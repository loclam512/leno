package main

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/suda/leno/parsers"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"

	timestampStartPattern  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:[ T]|$)`)
	timestampLevelPattern  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?)?\s+(TRACE|DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL)\b`)
	logfmtStartPattern     = regexp.MustCompile(`^[A-Za-z0-9_.-]+=[^\s]+(?:\s+[A-Za-z0-9_.-]+=.*)?$`)
	stackTraceMorePattern  = regexp.MustCompile(`^\.\.\. \d+ more$`)
	upperLevelStartPattern = regexp.MustCompile(`^(TRACE|DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL)\b`)
	prefixedLevelPattern   = regexp.MustCompile(`^(TRACE|DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL)\b`)
)

//go:embed all:public
var publicFS embed.FS

type historyEntry struct {
	id   int64
	data string
}

type historyResponse struct {
	Items      []json.RawMessage `json:"items"`
	NextBefore int64             `json:"next_before,omitempty"`
	HasMore    bool              `json:"has_more"`
	PageSize   int               `json:"page_size"`
}

type hub struct {
	mu      sync.Mutex
	clients map[chan string]struct{}
	history []historyEntry
	nextID  int64

	maxHistory int
	pageSize   int
}

func newHub(maxHistory, pageSize int) *hub {
	if maxHistory <= 0 {
		maxHistory = 10000
	}
	if pageSize <= 0 {
		pageSize = 1000
	}

	return &hub{
		clients:    make(map[chan string]struct{}),
		maxHistory: maxHistory,
		pageSize:   pageSize,
	}
}

func (h *hub) add(ch chan string) {
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) remove(ch chan string) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

func (h *hub) broadcast(line string) {
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	line = attachMessageID(line, id)
	h.history = append(h.history, historyEntry{id: id, data: line})
	if len(h.history) > h.maxHistory {
		h.history = h.history[len(h.history)-h.maxHistory:]
	}
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- line:
		default:
		}
	}
}

func (h *hub) historyPage(before int64, limit int) ([]string, int64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if limit <= 0 {
		limit = h.pageSize
	}
	if limit > h.maxHistory {
		limit = h.maxHistory
	}

	items := make([]string, 0, limit)
	var nextBefore int64
	hasMore := false

	for i := len(h.history) - 1; i >= 0; i-- {
		entry := h.history[i]
		if before > 0 && entry.id >= before {
			continue
		}
		if len(items) == limit {
			hasMore = true
			break
		}
		items = append(items, entry.data)
		nextBefore = entry.id
	}

	return items, nextBefore, hasMore
}

func main() {
	port := os.Getenv("LENO_PORT")
	if port == "" {
		port = "3000"
	}
	pageSize := envInt("LENO_HISTORY_PAGE_SIZE", 1000)
	maxHistory := envInt("LENO_HISTORY_BUFFER_SIZE", 10000)

	logFormat := flag.String("log-format", "", "Log format to parse (nginx, logfmt)")
	ingestURL := flag.String("ingest-url", "", "Forward stdin to a running leno instance")
	sourceName := flag.String("source-name", "", "Attach a source label to each log line")
	flag.Parse()

	if *ingestURL != "" {
		if err := forwardStream(os.Stdin, *ingestURL, *logFormat, *sourceName); err != nil {
			log.Fatal(err)
		}
		return
	}

	h := newHub(maxHistory, pageSize)
	go func() {
		if err := readStream(os.Stdin, *logFormat, *sourceName, h.broadcast); err != nil {
			log.Printf("stdin error: %v", err)
		}
	}()

	sub, err := fs.Sub(publicFS, "public")
	if err != nil {
		log.Fatalf("embed error: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, version)
	})
	mux.HandleFunc("/history", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		before, err := parseInt64Query(r, "before")
		if err != nil {
			http.Error(w, "invalid before parameter", http.StatusBadRequest)
			return
		}
		limit, err := parseIntQuery(r, "limit", pageSize)
		if err != nil {
			http.Error(w, "invalid limit parameter", http.StatusBadRequest)
			return
		}

		items, nextBefore, hasMore := h.historyPage(before, limit)
		resp := historyResponse{
			Items:    make([]json.RawMessage, 0, len(items)),
			HasMore:  hasMore,
			PageSize: pageSize,
		}
		if nextBefore > 0 {
			resp.NextBefore = nextBefore
		}
		for _, item := range items {
			resp.Items = append(resp.Items, json.RawMessage(item))
		}

		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/ingest", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		defer r.Body.Close()
		if err := readStream(
			r.Body,
			r.Header.Get("X-Leno-Log-Format"),
			r.Header.Get("X-Leno-Source"),
			h.broadcast,
		); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch := make(chan string, 16)
		h.add(ch)
		defer h.remove(ch)

		for {
			select {
			case line, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", line)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.Handle("/", http.FileServer(http.FS(sub)))

	log.Printf("Leno running on http://localhost:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func readStream(r io.Reader, logFormat, sourceName string, consume func(string)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	currentEntry := make([]string, 0, 16)
	flush := func() {
		if len(currentEntry) == 0 {
			return
		}
		consume(normalizeLine(strings.Join(currentEntry, "\n"), logFormat, sourceName))
		currentEntry = currentEntry[:0]
	}

	for scanner.Scan() {
		line := scanner.Text()
		payload := entryPayload(line)
		if len(currentEntry) == 0 {
			if payload == "" {
				continue
			}
			currentEntry = append(currentEntry, line)
			continue
		}

		if startsNewEntry(line, currentEntry[0]) {
			flush()
		}
		currentEntry = append(currentEntry, line)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	flush()
	return nil
}

func forwardStream(r io.Reader, ingestURL, logFormat, sourceName string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	return readStream(r, logFormat, sourceName, func(line string) {
		if err := postLine(client, ingestURL, line); err != nil {
			log.Fatal(err)
		}
	})
}

func postLine(client *http.Client, ingestURL, line string) error {
	req, err := http.NewRequest(http.MethodPost, ingestURL, bytes.NewBufferString(line+"\n"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("ingest failed: %s: %s", resp.Status, string(body))
	}

	return nil
}

func normalizeLine(line, logFormat, sourceName string) string {
	sourceName, line = extractSource(line, sourceName)

	switch logFormat {
	case "nginx":
		if parsed, ok := parsers.ParseNginx(line); ok {
			line = parsed
		}
	case "logfmt":
		if parsed, ok := parsers.ParseLogfmt(line); ok {
			line = parsed
		}
	}

	derivedLevel := extractLevel(line)
	payload := make(map[string]any)
	if err := json.Unmarshal([]byte(line), &payload); err == nil {
		if normalized := normalizeLevel(payload["level"]); normalized != "" {
			payload["level"] = normalized
		} else if derivedLevel != "" {
			payload["level"] = derivedLevel
		}
		if sourceName != "" {
			payload["source"] = sourceName
		}
		return mustMarshal(payload)
	}

	payload["message"] = line
	if derivedLevel != "" {
		payload["level"] = derivedLevel
	}
	if sourceName != "" {
		payload["source"] = sourceName
	}
	return mustMarshal(payload)
}

func normalizeLevel(raw any) string {
	if raw == nil {
		return ""
	}

	value := strings.ToLower(strings.TrimSpace(fmt.Sprint(raw)))
	if value == "" || value == "<nil>" {
		return ""
	}
	if value == "warning" {
		return "warn"
	}
	return value
}

func extractLevel(line string) string {
	payload := entryPayload(line)
	if payload == "" {
		return ""
	}

	if matches := timestampLevelPattern.FindStringSubmatch(payload); len(matches) == 2 {
		return normalizeLevel(matches[1])
	}

	trimmed := strings.TrimSpace(payload)
	if matches := prefixedLevelPattern.FindStringSubmatch(strings.ToUpper(trimmed)); len(matches) == 2 {
		return normalizeLevel(matches[1])
	}

	return ""
}

func attachMessageID(line string, id int64) string {
	payload := make(map[string]any)
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		payload["message"] = line
	}
	payload["_leno_id"] = id
	return mustMarshal(payload)
}

func extractSource(line, sourceName string) (string, string) {
	if sourceName != "" {
		return sourceName, line
	}
	if !strings.HasPrefix(line, "[") {
		return sourceName, line
	}

	closing := strings.Index(line, "]")
	if closing <= 1 {
		return sourceName, line
	}

	extracted := strings.TrimSpace(line[1:closing])
	rest := strings.TrimSpace(line[closing+1:])
	if extracted == "" {
		return sourceName, line
	}

	return extracted, rest
}

func entryPayload(line string) string {
	_, payload := extractSource(line, "")
	return strings.TrimSpace(payload)
}

func startsNewEntry(line, currentFirstLine string) bool {
	currentSource, _ := extractSource(currentFirstLine, "")
	nextSource, _ := extractSource(line, "")
	if currentSource != "" && nextSource != "" && currentSource != nextSource {
		return true
	}

	nextPayload := entryPayload(line)
	if nextPayload == "" {
		return false
	}
	if isExplicitEntryStart(nextPayload) {
		return true
	}
	if looksLikeContinuation(nextPayload) {
		return false
	}

	currentPayload := entryPayload(currentFirstLine)
	return !isExplicitEntryStart(currentPayload)
}

func isExplicitEntryStart(payload string) bool {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "{") && json.Valid([]byte(trimmed)) {
		return true
	}
	if timestampStartPattern.MatchString(trimmed) {
		return true
	}
	if logfmtStartPattern.MatchString(trimmed) {
		return true
	}
	return upperLevelStartPattern.MatchString(strings.ToUpper(trimmed))
}

func looksLikeContinuation(payload string) bool {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return true
	}

	leadingTrimmed := strings.TrimLeft(payload, " \t")
	switch {
	case strings.HasPrefix(leadingTrimmed, "at "):
		return true
	case strings.HasPrefix(trimmed, "Caused by:"):
		return true
	case strings.HasPrefix(trimmed, "Suppressed:"):
		return true
	case strings.HasPrefix(trimmed, "Wrapped by:"):
		return true
	case stackTraceMorePattern.MatchString(trimmed):
		return true
	default:
		return false
	}
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func parseIntQuery(r *http.Request, key string, fallback int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}

func parseInt64Query(r *http.Request, key string) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}

func mustMarshal(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		fallback, _ := json.Marshal(map[string]any{"message": fmt.Sprint(v)})
		return string(fallback)
	}
	return string(encoded)
}
