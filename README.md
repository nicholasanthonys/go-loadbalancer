# GoBalance

A Layer 4 (raw TCP) / Layer 7 (HTTP) load balancer written in Go, built to demonstrate systems-level
engineering — concurrency, networking, fault tolerance, and observability — rather than to compete
with nginx/HAProxy/Envoy on features or performance.

Full design rationale lives in [`docs/`](docs/):

- [`docs/PRD.md`](docs/PRD.md) — goals, non-goals, functional/non-functional requirements, milestones.
- [`docs/TECH_STACK.md`](docs/TECH_STACK.md) — architecture, project layout, library choices.
- [`docs/THEORY.md`](docs/THEORY.md) — the networking/concurrency theory behind each design decision.
- [`docs/TUTORIAL.md`](docs/TUTORIAL.md) — the phased build guide this project follows, with a
  per-phase progress table at the top.

## Status

Currently implemented:

- **L4 TCP proxy** ([`internal/proxy/l4.go`](internal/proxy/l4.go)) — accepts raw TCP connections,
  one goroutine per connection, relays bytes bidirectionally to a backend chosen by the listener's
  balancer. Optionally TLS-terminating (`*tls.Config` in, `nil` for plaintext).
- **L7 HTTP proxy** ([`internal/proxy/l7.go`](internal/proxy/l7.go)) — built on
  `httputil.ReverseProxy` with a custom `Rewrite` for backend selection; TLS termination is free
  here since `http.Server` decrypts below the handler layer.
- **Backend pool** ([`internal/pool`](internal/pool)) — backend addresses/weights plus
  concurrency-safe health state and an atomic active-connection counter per backend.
- **Six load-balancing algorithms** ([`internal/balancer`](internal/balancer)) — round robin,
  weighted round robin, least connections, weighted least connections, random, and
  power-of-two-choices, all interchangeable behind one `Balancer` interface. `balancer.New(name,
  pool)` constructs any of them from a config string.
- **Active health checking** ([`internal/healthcheck`](internal/healthcheck)) — TCP dial checks for
  L4 pools, HTTP GET + expected-status checks for L7 pools, both with hysteresis (N consecutive
  failures to mark unhealthy, M consecutive successes to recover).
- **TLS termination** — self-signed dev cert support, toggled at runtime with `-tls` rather than
  being TLS-only.
- **YAML configuration** ([`internal/config`](internal/config)) — `configs/example.yaml` defines any
  number of listeners, each with its own type (`l4`/`l7`), algorithm, backends, and health-check
  settings; `main.go` builds a pool/balancer/health-checker per listener from it and runs each on
  its own goroutine.

Not yet implemented: hot reload on config-file change (the config *loads* correctly today, but
editing it requires a restart), metrics/logging, graceful shutdown, and the Docker Compose demo. See
[`docs/TUTORIAL.md`](docs/TUTORIAL.md)'s progress table for exact phase-by-phase status.

## Running it

Requires Go 1.21+.

`-tls` defaults to `true`, so generate a self-signed dev certificate first (skip this if you plan to
run with `-tls=false`):

```bash
mkdir -p certs
openssl req -x509 -newkey rsa:2048 -nodes -keyout certs/key.pem -out certs/cert.pem -days 365 -subj "/CN=localhost"
```

To try it locally, start three dummy backends first. Giving each one distinct content makes
round-robin cycling visible directly in `curl`'s output, rather than needing to check each backend's
request log. Each backend also needs a `/healthz` file — the default config's L7 listener actively
health-checks that path and expects a `200`, and `python3 -m http.server` 404s on anything that
doesn't exist on disk:

```bash
mkdir -p /tmp/backend1 /tmp/backend2 /tmp/backend3
echo "Hello from backend 9101" > /tmp/backend1/index.html
echo "Hello from backend 9102" > /tmp/backend2/index.html
echo "Hello from backend 9103" > /tmp/backend3/index.html
echo ok > /tmp/backend1/healthz
echo ok > /tmp/backend2/healthz
echo ok > /tmp/backend3/healthz

python3 -m http.server 9101 --bind 127.0.0.1 --directory /tmp/backend1 &
python3 -m http.server 9102 --bind 127.0.0.1 --directory /tmp/backend2 &
python3 -m http.server 9103 --bind 127.0.0.1 --directory /tmp/backend3 &
```

Then, in another terminal:

```bash
go run ./cmd/gobalance
```

This reads [`configs/example.yaml`](configs/example.yaml) by default (override with `-config
path/to/file.yaml`) and starts an L4 listener on `localhost:9090` and an L7 listener on `:9091`,
both round-robining across the three backends above. Health checks run on a 5s interval and need 2
consecutive successes before a backend is used — allow ~10s after starting backends before traffic
stops getting `503 no healthy backends available`.

Send it traffic (TLS is on by default, hence `-k` to skip self-signed-cert verification; add
`-tls=false` to the `go run` command above and use plain `http://`/`nc` instead if you'd rather test
without TLS):

```bash
for i in 1 2 3 4 5 6; do curl -sk https://localhost:9091/index.html; done
```

Each response cycles through the three backends in order, e.g.:

```
Hello from backend 9101
Hello from backend 9102
Hello from backend 9103
Hello from backend 9101
Hello from backend 9102
Hello from backend 9103
```

The L4 listener proxies raw TCP the same way — `openssl s_client -connect localhost:9090` (or `nc
localhost 9090` with `-tls=false`) will get you a response from whichever backend port it picked,
though there's no HTTP framing to make the round-robin cycling as easy to eyeball as the L7 example
above.

## Testing

```bash
go test ./...
go test -race ./...
```

`-race` is treated as a hard requirement, not a nice-to-have — the pool's health state and
active-connection counts are read and written concurrently by the proxy loop and the health checker,
and that's exactly the kind of thing the race detector exists to catch.
