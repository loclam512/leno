VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build build-linux-amd64 dev clean lint format format-check ci

build:
	bun run build
	go build -ldflags "$(LDFLAGS)" -o leno .

build-linux-amd64:
	bun run build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o leno-linux-amd64 .

dev:
	bun run dev

lint:
	bun run lint
	go vet ./...

format:
	bun run format

format-check:
	bun run format:check

ci: format-check lint build

clean:
	rm -rf public/build
	rm -f leno
