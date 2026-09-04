# Tech Stack: GoBalance

## 1. Why Go

Go is the right tool for this specific project, not just a trendy choice, for reasons that are worth being able to articulate:

- **Goroutines are cheap enough to use one per connection.** A load balancer's core job is juggling thousands of concurrent, mostly-idle connections. Go's M:N scheduler multiplexes goroutines onto OS threads, so a "blocking" read on a connection doesn't block a thread — the runtime parks the goroutine and reuses the thread elsewhere. This gets you the throughput of an event loop (nginx, Envoy) with the readability of straight-line, blocking-style code, instead of callback or async/await chains.
- **The standard library already contains most of a load balancer.** `net`, `net/http`, `net/http/httputil`, `crypto/tls`, and `context` cover raw TCP proxying, HTTP reverse proxying, TLS termination, and cancellation/timeout propagation without pulling in a framework. This keeps the amount of "magic" low, which matters for a project meant to demonstrate understanding rather than library assembly.
- **Static binaries and fast startup.** `go build` produces a single self-contained binary — no runtime, no dependency resolution at deploy time. That makes the Docker image trivial (`FROM scratch` or `distroless`) and the demo fast to bring up.
- **Race detector.** `go test -race` and `go build -race` catch concurrent-access bugs (e.g. two goroutines updating a connection counter without synchronization) at test time instead of as a flaky bug discovered during a demo.
- **This is also the industry-standard choice.** Real load balancers and proxies — Traefik, Caddy, parts of Envoy's control plane tooling, countless internal company proxies — are written in Go for exactly these reasons. Building one in Go, rather than in a language chosen just because it's familiar, shows the choice was deliberate.

Go version: **1.24+** (current stable as of this writing). No language features specific to a newer minor version are required, but `log/slog` (structured logging, stdlib since 1.21) and generics (1.18+) are both used, so anything 1.21+ works.

## 2. Standard Library Components

| Package | Used for |
|---|---|
| `net` | Raw TCP listening/dialing for the L4 proxy; `net.Dialer` with timeouts for backend health checks. |
| `net/http`, `net/http/httputil` | HTTP server for the L7 listener; `httputil.ReverseProxy` as the request-forwarding engine, with a custom `Director`/`Rewrite` function for backend selection and a custom `Transport` for connection pooling control. |
| `crypto/tls` | TLS termination on listeners; certificate loading and optional hot-reload via `tls.Config.GetCertificate`. |
| `context` | Request-scoped cancellation and deadlines threaded through health checks and proxied requests. |
| `sync`, `sync/atomic` | Safe concurrent updates to per-backend connection counters and health state; `atomic.Int64` for hot-path counters instead of mutex-guarded ints where possible. |
| `log/slog` | Structured (JSON) logging — connection events, health transitions, config reloads — with levels and key/value fields instead of formatted strings. |
| `encoding/json` | Metrics/status API responses, structured log encoding. |
| `flag` | CLI flags (`-config`, `-listen`, `-log-level`) for local runs. |
| `os/signal` | Catching `SIGHUP` (config reload) and `SIGTERM`/`SIGINT` (graceful shutdown). |
| `time` | Health-check intervals/timeouts, request latency measurement. |

## 3. Third-Party Libraries

Kept deliberately small — the point is to show you understand the mechanics, not to demonstrate you can `go get` a framework. Each addition below is justified individually:

| Library | Purpose | Why this one |
|---|---|---|
| `github.com/prometheus/client_golang` | `/metrics` endpoint, counters/gauges/histograms | The de facto standard Go Prometheus client; using anything else would just be reinventing it worse. |
| `gopkg.in/yaml.v3` | Parsing the YAML config file | Standard, well-maintained YAML parser; `encoding/json` can't read YAML and hand-rolling a parser adds no learning value. |
| `github.com/fsnotify/fsnotify` | Watching the config file for changes to trigger hot reload | Cross-platform filesystem notifications; the alternative (polling `stat` in a loop) is worse in every way and teaches nothing extra. |
| `github.com/stretchr/testify` | `assert`/`require` in tests | Reduces test boilerplate; the LB logic itself, not the test framework, is what should be original work. |
| `golang.org/x/time/rate` | Optional token-bucket rate limiting on health-check dialing (avoid hammering a flapping backend) | Standard extended library from the Go team, not a random third party. |

Everything else — the proxy core, the algorithms, the health checker, the config reloader — is hand-written. That's the point of the project.

## 4. Architecture

```
                                   ┌─────────────────────────┐
                                   │        GoBalance          │
                                   │                          │
 clients ──TCP/TLS──▶ L4 Listener │  ┌──────────────┐        │
                                   │  │  Backend Pool │◀──┐    │
 clients ──HTTP/TLS─▶ L7 Listener │  │  (per listener)│   │    │
                                   │  └──────┬───────┘   │    │
                                   │         │            │    │
                                   │  ┌──────▼───────┐    │    │
                                   │  │  Algorithm    │    │    │
                                   │  │  (RR / LC /   │    │    │
                                   │  │  Weighted...) │    │    │
                                   │  └──────┬───────┘    │    │
                                   │         │            │    │
                                   │  ┌──────▼───────┐    │    │
                                   │  │Health Checker │────┘    │
                                   │  │(active+passive)│         │
                                   │  └──────────────┘          │
                                   │                          │
                                   │  /metrics   /healthz     │
                                   │  slog → stdout (JSON)    │
                                   └────────────┬─────────────┘
                                                │
                                    ┌───────────┼───────────┐
                                    ▼           ▼           ▼
                               backend-1   backend-2   backend-3
```

Each **listener** (L4 or L7) owns a reference to a **backend pool**: a slice of backend definitions (address, weight) plus shared, concurrency-safe state (health status, active connection count). The **algorithm** is a small interface (`Pick(pool) (*Backend, error)`) so round robin, least-connections, etc. are interchangeable strategies over the same pool — this is the one place a classic Strategy pattern is worth using. The **health checker** runs independently per pool, mutating shared backend state that both the algorithm and the proxy read.

## 5. Project Structure

```
gobalance/
├── cmd/
│   └── gobalance/
│       └── main.go              # wires config, listeners, checker, metrics; handles signals
├── internal/
│   ├── config/                  # YAML schema, validation, hot-reload watcher
│   ├── pool/                    # Backend, Pool types; thread-safe state
│   ├── balancer/                # Algorithm interface + round_robin.go, least_conn.go, weighted.go, random.go
│   ├── healthcheck/              # active + passive checkers, hysteresis logic
│   ├── proxy/
│   │   ├── l4.go                # raw TCP proxy loop
│   │   └── l7.go                # httputil.ReverseProxy wiring, header rewriting
│   ├── metrics/                 # Prometheus collectors
│   └── logging/                 # slog setup/helpers
├── configs/
│   └── example.yaml
├── deploy/
│   ├── Dockerfile
│   └── docker-compose.yaml      # gobalance + 3 sample backend servers
├── test/
│   └── integration/             # end-to-end tests spinning up real backends
├── go.mod
└── README.md
```

`internal/` is used deliberately — nothing here needs to be importable by other projects, and it signals a clean module boundary.

## 6. Testing Strategy

- **Unit tests** for each algorithm (deterministic distribution given a fixed backend set and call sequence) and for health-check state transitions (feed a fake dialer/HTTP client a scripted sequence of successes/failures, assert the hysteresis thresholds are honored).
- **Integration tests** in `test/integration/`: spin up several real `httptest.Server` backends, point a real GoBalance listener at them, and assert on actual request distribution and failover behavior — including killing a backend mid-test and asserting recovery.
- **Race detection**: `go test -race ./...` in CI (even a single GitHub Actions workflow is enough to show this is taken seriously).
- **Load testing**: [`vegeta`](https://github.com/tsenart/vegeta) or `hey` against the demo docker-compose setup to produce the throughput/latency numbers referenced in the PRD's success metrics, plus a scripted "kill a container mid-run" scenario to demonstrate failover under load.

## 7. Deployment

- **Dockerfile**: multi-stage build — `golang:1.24` builder stage compiling a static binary (`CGO_ENABLED=0`), copied into a `gcr.io/distroless/static` or `scratch` final image. Final image should be well under 20MB.
- **docker-compose.yaml**: GoBalance plus three trivial backend HTTP servers (can be `nginx` serving a static page with the container name baked in, or tiny Go/Python "hello from backend N" servers) so a reviewer can `docker compose up` and immediately curl the LB and watch it distribute across backends and survive one being killed.
- No Kubernetes packaging in v1 — it adds operational surface area without adding to what the project demonstrates. If this evolves into a v2, a Helm chart would be the natural next step.

## 8. Alternatives Considered (and why not)

- **Rust** — would demonstrate memory-safety chops but the async ecosystem (tokio) has a steeper learning curve for this scope, and Go's standard library maps more directly onto the L4/L7 proxy primitives needed here.
- **Node.js** — event loop concurrency model is a reasonable fit, but weaker standard-library support for raw TCP proxying and no compile-time race detection.
- **A framework like Traefik's internals as a library** — would undercut the entire point of the project, which is to build and understand the mechanics, not configure someone else's.
