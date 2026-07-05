# rest-pty

> Terminals as a service — drive PTY sessions over REST and MCP.

[![CICD](https://github.com/alwitt/rest-pty/actions/workflows/cicd.yaml/badge.svg)](https://github.com/alwitt/rest-pty/actions/workflows/cicd.yaml)
[![Go Version](https://img.shields.io/badge/go-1.26-00ADD8?logo=go)](https://go.dev/)
[![Go Report Card][ReportCard-Image]][ReportCard-Url]
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](./LICENSE)

[ReportCard-Url]: https://goreportcard.com/report/github.com/alwitt/rest-pty
[ReportCard-Image]: https://goreportcard.com/badge/github.com/alwitt/rest-pty

`rest-pty` wraps **pseudo-terminal (PTY) sessions behind a REST API**. You define named
sessions that each run a command, start and stop them on demand, feed them input, and read
back their scrollback output — all over HTTP. It is useful for orchestrating interactive or
long-running terminal programs from a service, a UI, or another automation.

## Overview

A **session** is a named, persisted object that describes a command to run and how to run it.
Sessions move between two states:

- **`IDLE`** — defined but not running.
- **`READY`** — running and accepting input.

Each session executes through one of two **drivers**:

- **`PTY`** — runs the command in a local pseudo-terminal on the host.
- **`DOCKER`** — runs the command inside a hardened, isolated container (read-only root
  filesystem, all Linux capabilities dropped, no networking by default).

Session metadata is persisted in **SQLite**, while input and output are streamed through
**Redis ring buffers**, which also retain a bounded scrollback history you can read at any
time.

## Features

- **Two execution drivers** — local PTY or hardened Docker container.
- **Hardened-by-default containers** — read-only rootfs, dropped capabilities,
  no-new-privileges, and no network access unless explicitly opted in.
- **Persistent sessions** — session definitions survive restarts via SQLite.
- **Scrollback** — bounded output history backed by Redis ring buffers.
- **OpenAPI spec** — authoritative schema in [`docs/swagger.yaml`](./docs/swagger.yaml),
  with a generated TypeScript client SDK target.
- **Observability** — Prometheus metrics endpoint plus health and readiness probes.

## Architecture

```
            HTTP                 ┌──────────────┐
 client ───────────► REST API ─► │   Manager    │
                                 └──────┬───────┘
                                        │
                                 ┌──────▼───────┐
                                 │    Runner    │   (one per running session)
                                 └──────┬───────┘
                                        │
                                 ┌──────▼───────┐
                                 │    Driver    │  PTY  │  Docker
                                 └──────┬───────┘
                                        │
           input  ──► Redis ring buffer ──► process stdin
           output ◄── Redis ring buffer ◄── process stdout/stderr

           session metadata ◄──► SQLite
```

The REST API delegates lifecycle operations to a **Manager**, which spins up a **Runner** for
each started session. The Runner drives a **PTY** or **Docker** core process, bridging its
stdin/stdout through Redis ring buffers. Session definitions are persisted in SQLite.

## Requirements

- **Go 1.26** (to build from source)
- A reachable **Redis** instance (used for session I/O buffers)
- **SQLite** — the database file is created automatically
- The **Docker daemon** — only required when using the `DOCKER` driver

## Quick start

1. **Start Redis** (a local development instance via the bundled compose stack, listening on
   `127.0.0.1:6479`):

   ```bash
   make up
   ```

2. **Build the binary**:

   ```bash
   go build -o rest-pty .
   # or: make build
   ```

3. **Create a config file** (`config.yml`):

   ```yaml
   metrics:
     service:
       listenOn: 127.0.0.1
       appPort: 3001

   api:
     service:
       listenOn: 127.0.0.1
       appPort: 38281

   redis:
     host: 127.0.0.1
     port: 6479
     dbNumber: 0

   sqlite:
     file: ./tmp/rest-pty.db
   ```

4. **Run the server**:

   ```bash
   ./rest-pty -l info server -c config.yml
   ```

   By default the REST API listens on port **38281** and the metrics endpoint on **3001**.

## Configuration

The server is configured with a YAML file (loaded via [Viper](https://github.com/spf13/viper))
and a few CLI flags. Any value omitted from the config falls back to a built-in default
(see `InstallDefaultServerConfigValues` in [`models/configs.go`](./models/configs.go)).

### Config file

```yaml
# Prometheus metrics server
metrics:
  service:
    listenOn: 0.0.0.0        # bind address
    appPort: 3001            # port
    timeoutSecs:
      read: 60
      write: 60
      idle: 60
  metricsEndpoint: /metrics  # path the metrics are exposed on
  maxRequests: 4             # max parallel metrics requests
  features:
    enableAppMetrics: false  # Go runtime metrics
    enableHTTPMetrics: true  # HTTP request metrics

# REST API server
api:
  service:
    listenOn: 0.0.0.0
    appPort: 38281
    timeoutSecs:
      read: 60
      write: 0               # 0 = no timeout (needed for long output reads)
      idle: 0
  apis:
    endPoint:
      pathPrefix: /          # base path prepended to all routes
    requestLogging:
      logLevel: warn
      healthLogLevel: debug
      requestIDHeader: X-Request-ID
    enableMCP: false           # expose the MCP endpoint (see "MCP API")

# Redis connection (used for session I/O buffers)
redis:
  host: 127.0.0.1
  port: 6479
  dbNumber: 0

# SQLite persistence (created automatically)
sqlite:
  file: ./tmp/rest-pty.db
```

### CLI flags

| Flag | Alias | Env var | Default | Description |
|------|-------|---------|---------|-------------|
| `--log-level` | `-l` | `LOG_LEVEL` | `warn` | Log level: `debug`, `info`, `warn`, `error` |
| `--json-log` | `-j` | `LOG_AS_JSON` | `false` | Emit logs as JSON |
| `--config-file` | `-c` | `CONFIG_FILE` | — | **Required.** Path to the server config file (`server` command) |

```bash
./rest-pty -l info server -c config.yml
```

## REST API

The authoritative API schema is the OpenAPI spec at
[`docs/swagger.yaml`](./docs/swagger.yaml). The summary below groups the available endpoints;
all `v1` routes are relative to the configured `pathPrefix`.

### Endpoint summary

| Area | Method & path |
|------|---------------|
| Health | `GET /liveness/alive`, `GET /liveness/ready` |
| Sessions | `POST /v1/sessions`, `GET /v1/sessions` |
| Session | `GET /v1/sessions/{name}`, `DELETE /v1/sessions/{name}` |
| Attributes | `PUT /v1/sessions/{name}/{command,driver,name,description,output-buf-cap}` |
| Lifecycle | `POST /v1/sessions/{name}/start`, `POST /v1/sessions/{name}/stop` |
| Input | `POST /v1/sessions/{name}/io/input/commands` |
| Output | `GET /v1/sessions/{name}/io/output/{chunk,newest,tail}` |

> Session names may contain only letters, digits, and hyphens (`^[a-zA-Z0-9-]+$`).

### Worked example

The flow below creates a PTY session running `bash`, starts it, runs a command, reads the
output, then stops and deletes the session. The examples assume the API is on
`http://localhost:38281`.

**1. Create a session**

```bash
curl -X POST http://localhost:38281/v1/sessions \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "demo",
    "description": "interactive bash shell",
    "command": { "cmd": "bash", "args": [] },
    "io_buf_cap": 65536,
    "driver": "PTY",
    "driver_metadata": { "display_rows": 40, "display_cols": 120 }
  }'
```

`io_buf_cap` is the scrollback buffer capacity in bytes and must be at least `16384`.

**2. Start it** (transitions `IDLE` → `READY`)

```bash
curl -X POST http://localhost:38281/v1/sessions/demo/start
```

**3. Send input.** Commands are submitted as a list. Use a `TEXT` command for literal input
and an `ENTRY` command to press Enter. (Other supported types: `CTRL` for control characters
such as CTRL+C, and `RAW` for Base64-encoded raw key input.)

```bash
curl -X POST http://localhost:38281/v1/sessions/demo/io/input/commands \
  -H 'Content-Type: application/json' \
  -d '{
    "commands": [
      { "type": "TEXT", "content": "echo hello from rest-pty" },
      { "type": "ENTRY" }
    ]
  }'
```

**4. Read the newest output** (`limit` is the number of bytes to read from the tail):

```bash
curl 'http://localhost:38281/v1/sessions/demo/io/output/newest?limit=4096'
```

To page through the full scrollback instead, use `output/chunk?offset=<n>&limit=<n>`.

**5. Stop and delete**

```bash
curl -X POST   http://localhost:38281/v1/sessions/demo/stop
curl -X DELETE http://localhost:38281/v1/sessions/demo
```

## MCP API

Alongside the REST API, `rest-pty` can expose the session surface to AI agents as
[Model Context Protocol](https://modelcontextprotocol.io) tools. This lets an agent define,
start, drive, and read back sandboxed terminal sessions using the same core the REST API
uses.

### Enabling it

The MCP endpoint is **disabled by default**. Turn it on with `enableMCP` under `api.apis`:

```yaml
api:
  apis:
    enableMCP: true
```

Once enabled, an MCP server (streamable HTTP transport, stateless) is mounted at
**`POST /v1/mcp`**, relative to the configured `pathPrefix`.

### Tools

The endpoint registers the following tools. Their input schemas mirror the REST DTOs (and
advertise the same enumerated values and validation rules).

**Session management**

| Tool | Purpose |
|------|---------|
| `define_new_session` | Define a new session (always docker-backed and hardened — see below) |
| `list_sessions` | List sessions, optionally filtered by name, driver, or state |
| `get_session` | Fetch a single session by name |
| `update_session_output_buffer_capacity` | Change an IDLE session's scrollback capacity (clears buffered output) |
| `update_session_command` | Change the command an IDLE session runs |
| `update_session_name` | Rename a session |
| `update_session_description` | Change (or clear) a session's description |
| `delete_session` | Delete an IDLE session |
| `start_session` | Start a session (`IDLE` → `READY`), synchronously |
| `stop_session` | Stop a session (`READY` → `IDLE`), synchronously |

**Session IO**

| Tool | Purpose |
|------|---------|
| `submit_user_input` | Submit a batch of keystroke input events to a READY session's STDIN |
| `read_session_output_chunk` | Read output from a given stream offset |
| `read_session_output_newest` | Read the most recently written output bytes |

### How it differs from the REST API

The MCP surface is a deliberately narrowed, agent-safe projection of the REST API:

- **Docker-only, hardened sessions.** `define_new_session` fixes the driver to `DOCKER`;
  there is no driver-type selection and no raw driver metadata. Only a restricted set of
  docker settings is exposed (image, TTY size, working dir, writable dirs, network mode,
  published ports, extra hosts, environment). Host bind-mounts, added Linux capabilities,
  and the hardening toggles are **not** exposed and keep their hardened defaults. If a
  session needs more, an operator can adjust it out-of-band via the REST API.
- **Always synchronous.** There is no equivalent of the REST `block` query parameter; each
  tool call is a discrete request/response, so operations always complete before returning.
- **`inputs`, not `command`.** `submit_user_input` takes `inputs` — keystroke-level input
  events fed to the running process's STDIN — named distinctly from the `{cmd, args}`
  command a session is *defined* to run, to keep the two from being conflated.
- **ANSI-stripped, structured output reads.** The output-read tools always strip ANSI
  escape sequences and return the decoded text both as a text content block and as
  structured fields (`output`, `actual_offset`, `read`) so an agent can advance its read
  offset. There is no MCP equivalent of the SSE `tail` stream.

## Docker driver

When a session uses `"driver": "DOCKER"`, the command runs inside a container that is
**hardened by default**. Unless overridden, the container:

- mounts its **root filesystem read-only**,
- **drops all Linux capabilities** and sets **no-new-privileges**,
- has **no network access** (`network_mode: none`),
- runs as **`nobody:nogroup`** in working directory **`/tmp`**,
- is limited to a **32m** memory reservation / **128m** hard limit,
- is **removed on exit**, and
- is stopped with **`SIGINT`**.

These defaults are defined by `SessionDriverDockerParams` in
[`models/sessions.go`](./models/sessions.go). The `driver_metadata` object accepts:

| Field | Description |
|-------|-------------|
| `image` | **Required.** Container image to run |
| `display_rows` / `display_cols` | TTY size (min 30 rows / 80 cols) |
| `writable_dirs` | tmpfs mounts that provide writable paths within the read-only rootfs |
| `host_mounts` | host paths bind-mounted in (read-only by default) |
| `add_caps` | Linux capabilities to add back (e.g. `NET_BIND_SERVICE`) |
| `network_mode` | container network mode; must be routable to publish ports |
| `publish_ports` | container ports published to the host for inbound connections |
| `extra_hosts` | extra host-to-IP mappings injected into `/etc/hosts` |
| `environment` | environment variables for the container process |
| `mem_reservation` / `mem_limit` | soft/hard memory limits (e.g. `"32m"`, `"128m"`) |
| `run_as_user` / `run_as_group` / `working_dir` | process identity and working directory |
| `stop_signal` | signal used to stop the process (`SIGINT`, `SIGTERM`, `SIGQUIT`, `SIGHUP`, `SIGKILL`) |
| `read_only_rootfs` / `drop_all_caps` / `no_new_privileges` / `remove_on_exit` | hardening toggles (each defaults to `true`) |

Example `driver_metadata` for a network-isolated container with a writable scratch directory:

```json
{
  "image": "alpine:3.20",
  "display_rows": 40,
  "display_cols": 120,
  "writable_dirs": [{ "path": "/work", "tmpfs_size": 33554432 }],
  "environment": [{ "name": "TZ", "value": "UTC" }]
}
```

## Development

Common tasks are wrapped in the [`Makefile`](./Makefile):

- `make build` — build the binary
- `make test` — run unit tests
- `make doc` — regenerate the OpenAPI spec in `docs/`
- `make ts-sdk` — generate the TypeScript client SDK

## License

Released under the [MIT License](./LICENSE).
