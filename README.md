# GoBalance

A Layer 4 (raw TCP) / Layer 7 (HTTP) load balancer written in Go, built to demonstrate systems-level
engineering — concurrency, networking, fault tolerance, and observability — rather than to compete
with nginx/HAProxy/Envoy on features or performance.

Full design rationale lives in [`docs/`](docs/):

- [`docs/PRD.md`](docs/PRD.md) — goals, non-goals, functional/non-functional requirements, milestones.
- [`docs/TECH_STACK.md`](docs/TECH_STACK.md) — architecture, project layout, library choices.
- [`docs/THEORY.md`](docs/THEORY.md) — the networking/concurrency theory behind each design decision.
- [`docs/TUTORIAL.md`](docs/TUTORIAL.md) — the phased build guide this project follows.

## Status

Work in progress. Currently implemented:

- **L4 TCP proxy** ([`internal/proxy/l4.go`](internal/proxy/l4.go)) — accepts raw TCP connections,
  one goroutine per connection, and relays bytes bidirectionally to a backend chosen by the active
  balancer.
- **Backend pool** ([`internal/pool`](internal/pool)) — holds the list of backend addresses.
- **Round robin balancer** ([`internal/balancer`](internal/balancer)) — cycles through backends in
  order using an atomic counter, safe under concurrent access.

Not yet implemented: health checking, additional algorithms (weighted round robin, least connections),
the L7/HTTP proxy, TLS termination, YAML configuration and hot reload, metrics/logging, graceful
shutdown, and the Docker Compose demo. See [`docs/PRD.md` §9](docs/PRD.md) for the full milestone list.

## Running it

Requires Go 1.21+.

```bash
go run ./cmd/gobalance
```

This starts the load balancer listening on `localhost:9090` and round-robins across a hardcoded
backend list (`localhost:9101`, `9102`, `9103`, configured in
[`cmd/gobalance/main.go`](cmd/gobalance/main.go)).

To try it locally, start three dummy backends first. Giving each one distinct content makes the
round-robin cycling visible directly in `curl`'s output, rather than needing to check each
backend's request log:

```bash
mkdir -p /tmp/backend1 /tmp/backend2 /tmp/backend3
echo "Hello from backend 9101" > /tmp/backend1/index.html
echo "Hello from backend 9102" > /tmp/backend2/index.html
echo "Hello from backend 9103" > /tmp/backend3/index.html

python3 -m http.server 9101 --bind 127.0.0.1 --directory /tmp/backend1 &
python3 -m http.server 9102 --bind 127.0.0.1 --directory /tmp/backend2 &
python3 -m http.server 9103 --bind 127.0.0.1 --directory /tmp/backend3 &
```

Then, in another terminal:

```bash
go run ./cmd/gobalance
```

And send it traffic:

```bash
for i in 1 2 3 4 5 6; do curl -s localhost:9090/index.html; done
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

## Testing

```bash
go test ./...
go test -race ./...
```
