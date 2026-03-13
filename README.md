<div align="center">
	<img src="assets/logo.png">
	<p>
		<b>Command line log viewer with a web UI</b>
	</p>
	<br>
	<a href="https://contributionswelcome.org/"><img src="https://img.shields.io/badge/contributions-welcome-7dcfef" /></a>
	<a href="https://choosealicense.com/licenses/mit/"><img src="https://img.shields.io/github/license/suda/go-gooey" /></a>
	<br>
	<br>
</div>

Leño is a log viewer with a web UI. It can ingest JSON lines, plain text logs, `logfmt`, and nginx access logs, then stream them to the browser in real time.

It now supports:

- live streaming over Server-Sent Events
- buffered history pages for browser reloads
- infinite scroll for older buffered logs
- service/source filtering when logs include a `source` field or a `[service-name]` prefix

![](./assets/screenshot.png)

## Installation

Download the latest binary for your platform from the [releases page](https://github.com/suda/leno/releases) and place it somewhere on your `$PATH`.

Or build from source:

```sh
git clone https://github.com/suda/leno
cd leno
bun install
make build
```

This produces a single `./leno` binary with the web UI embedded.

## Login

The web UI uses the built-in monitor account:

```txt
username: monitor
password: gorilla@esim#
```

## Basic usage

Pipe any process into `leno`:

```sh
./myapp | leno
node server.js | leno
java -jar app.jar | leno
```

By default Leño runs on `http://localhost:3000`.

Use a custom port with `LENO_PORT`:

```sh
LENO_PORT=8080 ./myapp | leno
```

## Supported input formats

### Plain text

Plain text lines are wrapped into JSON like this:

```json
{"message":"..."}
```

### JSON logs

If the input is already JSON, Leño preserves the fields and streams them directly.

### logfmt

Use `--log-format=logfmt` for `key=value` logs:

```sh
./myapp | leno --log-format=logfmt
```

### nginx

Use `--log-format=nginx` for nginx or ingress-nginx access logs:

```sh
tail -f /var/log/nginx/access.log | leno --log-format=nginx
```

## Browser behavior

When the UI opens, Leño:

1. loads the latest buffered history page from `/history`
2. opens a live `/events` stream for new logs
3. loads older buffered pages as you scroll down

The default history page size is `1000`.

Configure history behavior with:

```sh
LENO_HISTORY_PAGE_SIZE=1000
LENO_HISTORY_BUFFER_SIZE=20000
```

- `LENO_HISTORY_PAGE_SIZE`: how many logs to load per page in the browser
- `LENO_HISTORY_BUFFER_SIZE`: how many recent logs the server keeps in memory for history replay

Important: this is an in-memory history buffer, not long-term storage. For persistent logs, keep your application logs on disk or in journald and feed them into Leño.

## Service grouping

The sidebar "Services" section is driven by the `source` field.

Leño can derive `source` in two ways:

1. Your logs already contain JSON with a `source` field:

```json
{"source":"gorilla-core.service","level":"INFO","message":"started"}
```

2. Your log lines are prefixed like this:

```txt
[gorilla-core.service] 2026-03-12 09:30:00,123 INFO started
```

If your logs do not include either of those, Leño groups them under:

```txt
unknown
```

## Shared log file pattern

A common EC2/systemd setup is:

- multiple services append to one shared file such as `/opt/apps/logs/all-services.log`
- each service prefixes every line with its service name
- Leño tails that file

Example service output line:

```txt
[gorilla-core.service] 2026-03-12 09:30:00,123 INFO started
```

Example service unit pattern:

```ini
[Service]
ExecStart=/bin/bash -lc 'exec /usr/bin/java -jar /opt/apps/gorilla-core/current/app.jar 2>&1 | sed -u "s/^/[gorilla-core.service] /" >> /opt/apps/logs/all-services.log'
```

Then run Leño against the shared file:

```sh
tail -n 5000 -F /opt/apps/logs/all-services.log | LENO_PORT=8080 leno
```

With that setup:

- the browser loads the latest buffered logs first
- new logs continue streaming live
- the sidebar can filter by service name

## Receiver mode

Leño also supports forwarding stdin into another running Leño instance:

```sh
java -jar app.jar | leno --ingest-url http://127.0.0.1:3000/ingest --source-name gorilla-core.service
```

That mode is useful when you want a dedicated receiver process, but for persistent EC2 setups the shared-file or journald model is usually simpler.

## Journald / systemd example

If your service already writes to journald, you can feed that into Leño:

```sh
journalctl -n 2000 -f -u gorilla-core.service -o cat | leno
```

## EC2 example

A practical EC2 deployment usually looks like this:

1. Application services write to `/opt/apps/logs/all-services.log`
2. Each service prefixes its own lines with `[service-name]`
3. A systemd unit runs:

```sh
tail -n 5000 -F /opt/apps/logs/all-services.log | /opt/leno/bin/leno
```

4. Nginx proxies your public domain to Leño
5. The browser loads `/history` once, then keeps `/events` open for new logs

## Development

Requirements:

- Go 1.21+
- Bun

Commands:

```sh
bun install
make build              # build embedded frontend + Go binary
make build-linux-amd64  # cross-compile Linux amd64 binary
make dev                # frontend dev server
make lint               # frontend lint + go vet
make format             # frontend format
make format-check       # frontend format check
make clean
```

## Notes

- `source` is required for per-service sidebar grouping.
- Plain text logs work, but structured JSON logs give a better filtering experience.
- History is buffered in memory; keep a durable log source if you need persistence beyond the running Leño process.

## Credits

Leño's logo is based on [log by Smalllike](https://thenounproject.com/term/log/2784204) from the Noun Project.
