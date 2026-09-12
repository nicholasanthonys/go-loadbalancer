# Tutorial: Building GoBalance Step by Step

This is a hands-on build guide, phased to match the milestones in the PRD. Each phase has a goal, the concepts it exercises (cross-reference THEORY.md), code to write, and a checkpoint to verify before moving on. Snippets show the core mechanics — you're meant to type them out and fill in the surrounding wiring yourself, not copy-paste a finished repo.

Prerequisite: Go 1.24+ installed (`go version` to confirm), basic familiarity with goroutines and channels.

---

## Progress

| Phase | Status | Notes |
|---|---|---|
| 0 — Project Setup | Done | `go.mod`, directory tree, `cmd/gobalance/main.go` all in place. |
| 1 — Minimal L4 TCP Proxy (Round Robin) | Done | `internal/pool`, `internal/balancer` (round robin), `internal/proxy/l4.go` all written and wired up in `main.go`; manually verified with `curl` against dummy backends; `internal/balancer/round_robin_test.go` covers exact cycling order and the empty-pool error case, passing under `-race`. |
| 2 — Active Health Checks (TCP) | Done | `internal/healthcheck/tcp.go`, `Backend` health state + hysteresis, `Pool.Healthy()` filtering, all wired into `main.go`; `internal/pool/pool_test.go` covers the hysteresis threshold logic directly. |
| 3 — L7 HTTP Reverse Proxy | Done | `internal/proxy/l7.go` implements `Rewrite`, header forwarding, and a context-stashing trick to route `Pick()` failures through `ErrorHandler`; wired in as a second listener on `:9091` in `main.go`, sharing the pool/balancer with L4. |
| 4 — More Algorithms | Done | `ActiveConns` tracking wired into `l4.go`; all six algorithms implemented — `RoundRobin`, `LeastConn`, `WeightedRoundRobin`, `WeightedLeastConn`, `Random`, `PowerOfTwoChoices`. `round_robin_test.go`, `least_conn_test.go`, `weighted_round_robin_test.go`, `random_test.go`, and `power_of_two_test.go` all pass under `-race`; `weighted_least_conn` has no dedicated test yet. |
| 5 — HTTP Health Checks + TLS Termination | Done | `internal/healthcheck/http.go` (`HTTPChecker`) mirrors `TCPChecker`'s hysteresis shape via the shared `Checker` interface; `http_test.go` drives the healthy→unhealthy→healthy flip end-to-end against a real `httptest.Server`. TLS termination added to both listeners: `internal/proxy/l4.go` takes an optional `*tls.Config` (`nil` = plaintext); L7 needed no code changes since `http.Server` decrypts transparently below the handler. `main.go`'s `-tls` flag makes plaintext/TLS a real runtime toggle rather than TLS-only. |
| 6 — Configuration File + Hot Reload | Done | **Config file:** `internal/config/config.go` defines `Config`/`Listener`/`HealthCheck`/`Backend` with `yaml.v3` tags, a custom `Duration` type so fields can be written as `"5s"`, `Load()` with validation, and `configs/example.yaml`. `cmd/gobalance/main.go` builds a pool/balancer/health-checker per listener from the loaded config via `balancer.New(name, pool)`, each on its own goroutine. **Hot reload:** `config.Store` (`atomic.Pointer[Config]`) holds the live config; `Reload()` re-reads + re-validates and swaps only on success, so a bad edit leaves the old config serving. `pool.Pool` has an `RWMutex`-guarded `SetBackends([]BackendSpec)` that reuses the existing `*Backend` for surviving addresses (health/hysteresis/active-conn state carries over) and starts genuinely new addresses unhealthy. `Backend.weight` became an `atomic.Int64` (`Weight()`/`SetWeight()`) since a reload now writes it while the weighted algorithms read it. `main.go` holds a listener-name → `*pool.Pool` registry plus a `started` map of each listener's launch config, and an `applyConfig` that pushes backend-list/weight changes onto the running pools — logging and ignoring algorithm/type changes (those need a restart). Two triggers feed one `reload()` closure: `SIGHUP` and an `fsnotify` watch on the config file's *directory* (editors rename-replace), debounced 200ms. `test/integration/reload_test.go` hammers a live L7 listener with 8 traffic goroutines while a 9th churns the backend set/weights ~300×, asserting zero dropped requests, clean under `-race`. |
| 7 — Observability: Logging + Metrics | Done | `internal/logging/logging.go` sets up a shared `slog.Logger`; request/reload/health-transition log lines wired through `l4.go`, `l7.go`, `main.go`, and both health checkers. `internal/metrics/metrics.go` defines `RequestsTotal` (listener+status), `RequestDuration` (listener), and `ReloadsTotal` (result) counters/histogram; `internal/metrics/collector.go`'s `BackendCollector` reads live per-backend active-conn and healthy-state off each pool at scrape time rather than caching it, served on `:9100/metrics`. `collector_test.go` covers the metric shapes and confirms state is read live (not cached), passing under `-race`. |
| 8 — Graceful Shutdown | Done | `internal/proxy/l4.go`'s `L4Server` gains `ListenAndServe`/`Serve`/`Shutdown`, mirroring `net/http.Server`'s shape: `Serve` tracks in-flight connections in a `sync.WaitGroup`, and a `closed` flag tells it a `Shutdown`-triggered `Accept` error apart from a real one. `main.go` wraps L7 and the metrics server in `*http.Server` values, adds a `shutdownableServer` interface so all three listener kinds drain identically, and replaces the old `select{}` with a `SIGTERM`/`SIGINT` handler that waits (bounded by the new `-shutdown-timeout` flag, default 30s) for every listener's `Shutdown` to return — run concurrently so one slow listener can't eat another's share of the timeout. `internal/proxy/l4_test.go`'s `TestL4Server_ShutdownDrainsInFlightConnections` automates the checkpoint scenario, passing under `-race`. |
| 9 — Race Audit + Load Testing | Done | `go test -race ./...` clean across the whole suite. `scripts/loadtest.sh` automates the load test end-to-end (dummy backends, `-race` gobalance build, three `vegeta` scenarios) — see README's "Load testing" section for the methodology and final numbers (p99 ~1.4ms at 2,000 req/s; reload-under-load and backend-kill scenarios both within PRD targets). Getting a clean number took two rounds of fixing the *test harness*, not gobalance: the stock dummy backend (`python3 -m http.server`) doesn't do HTTP keep-alive, so at load it exhausted local ephemeral ports and produced multi-second latencies that were a test artifact, not proxy overhead; after swapping in a keep-alive backend, a second artifact appeared — a flat ~40ms latency floor on every request, the classic Nagle's-algorithm + delayed-ACK interaction, fixed by setting `TCP_NODELAY` on the dummy backend's socket. Also tuned `configs/example.yaml`'s health check (`interval: 5s`→`1s`, `unhealthy_threshold: 3`→`2`) since the original hysteresis settings took up to ~15s to evict a dead backend — 3 check intervals, not the "one health-check interval" the PRD describes — which swamped a 20s test window with failures that weren't representative of steady-state resilience. |
| 10 — Containerize and Package for Demo | Not started | `deploy/` is empty. |
| 11 — Polish for Portfolio | Not started | |

**Pick up here:** Phase 10 — containerize and package for demo. Write `deploy/Dockerfile` and `deploy/docker-compose.yaml` wiring gobalance up with three sample backend containers, replacing the ad-hoc `python3 -m http.server` dummy backends used for manual/load testing so far.

---

## Phase 0 — Project Setup

```bash
mkdir gobalance && cd gobalance
go mod init github.com/<you>/gobalance
mkdir -p cmd/gobalance internal/{config,pool,balancer,healthcheck,proxy,metrics,logging} configs deploy test/integration
```

Create `cmd/gobalance/main.go` with a placeholder `func main() { fmt.Println("gobalance starting") }` and confirm `go run ./cmd/gobalance` works. Commit. This sounds trivial but it's the checkpoint that your module path, directory layout, and Go toolchain are all correctly wired before any real logic goes in.

---

## Phase 1 — Minimal L4 TCP Proxy (Round Robin)

**Goal:** accept TCP connections and relay them to a hardcoded list of backends in round-robin order.

**Concepts:** `net.Listener`, one-goroutine-per-connection, bidirectional byte copying, `atomic` counters (THEORY.md §5, §3).

The code below is the verified, currently-compiling state of the project at the end of Phase 1 (before health checking exists — that's Phase 2, and it extends `pool.go` further). Four files:

```go
// internal/pool/pool.go
package pool

type Backend struct {
    Addr   string
    Weight int
}

type Pool struct {
    Backends []*Backend
}

func New(addrs []string) *Pool {
    p := &Pool{}
    for _, addr := range addrs {
        p.Backends = append(p.Backends, &Backend{Addr: addr, Weight: 1})
    }
    return p
}

// Healthy is a placeholder until Phase 2 adds real health tracking —
// for now every backend is considered eligible.
func (p *Pool) Healthy() []*Backend {
    return p.Backends
}
```

```go
// internal/balancer/balancer.go
package balancer

import (
    "errors"

    "github.com/nicholasanthonys/gobalance/internal/pool"
)

var ErrNoHealthyBackends = errors.New("no healthy backends available")

type Balancer interface {
    Pick() (*pool.Backend, error)
}
```

```go
// internal/balancer/round_robin.go
package balancer

import (
    "sync/atomic"

    "github.com/nicholasanthonys/gobalance/internal/pool"
)

type RoundRobin struct {
    pool *pool.Pool
    next uint64
}

func NewRoundRobin(pool *pool.Pool) *RoundRobin {
    return &RoundRobin{
        pool: pool,
        next: 0,
    }
}

func (r *RoundRobin) Pick() (*pool.Backend, error) {
    backends := r.pool.Healthy()
    if len(backends) == 0 {
        return nil, ErrNoHealthyBackends
    }

    // prevent data race by using atomic operations to increment the next index
    n := atomic.AddUint64(&r.next, 1)

    // call 1: n=1 → (1-1)%3 = 0 → A
    // call 2: n=2 → (2-1)%3 = 1 → B
    // call 3: n=3 → (3-1)%3 = 2 → C
    // call 4: n=4 → (4-1)%3 = 0 → A again

    return backends[(n-1)%uint64(len(backends))], nil
}
```

Note the `RoundRobin` struct's fields are unexported (`pool`, `next`). That's deliberate encapsulation, but it means another package *cannot* build one with a struct literal (`balancer.RoundRobin{pool: p}` won't compile outside this package) — `NewRoundRobin` is the only way in. This trips people up constantly; if you ever see `cannot refer to unexported field`, this is why.

```go
// internal/proxy/l4.go
package proxy

import (
    "io"
    "net"
    "sync"
    "time"

    "github.com/nicholasanthonys/gobalance/internal/balancer"
)

func ServeL4(listenAddr string, b balancer.Balancer) error {
    ln, err := net.Listen("tcp", listenAddr)
    if err != nil {
        return err
    }

    for {
        conn, err := ln.Accept()
        if err != nil {
            continue // log and keep serving
        }
        go handleConn(conn, b)
    }
}

func handleConn(client net.Conn, b balancer.Balancer) {
    defer client.Close()
    backend, err := b.Pick()
    if err != nil {
        return
    }
    upstream, err := net.DialTimeout("tcp", backend.Addr, 5*time.Second)
    if err != nil {
        return
    }
    defer upstream.Close()

    var wg sync.WaitGroup
    wg.Add(2)
    go func() {
        defer wg.Done()
        io.Copy(upstream, client)
    }()
    go func() {
        defer wg.Done()
        io.Copy(client, upstream)
    }()
    wg.Wait()
}
```

The two `io.Copy` calls running in their own goroutines are the entire "relay" — this is the smart-cable behavior described in THEORY.md §2. Each direction of the pipe is independent, so a slow client reading a fast response doesn't block the request direction.

And `main.go`, wiring the three pieces together:

```go
// cmd/gobalance/main.go
package main

import (
    "fmt"

    "github.com/nicholasanthonys/gobalance/internal/balancer"
    "github.com/nicholasanthonys/gobalance/internal/pool"
    "github.com/nicholasanthonys/gobalance/internal/proxy"
)

func main() {
    fmt.Println("gobalance starting...")

    p := pool.New([]string{"localhost:9101", "localhost:9102", "localhost:9103"})
    rr := balancer.NewRoundRobin(p)

    fmt.Println("L4 listening on :9090")
    if err := proxy.ServeL4("localhost:9090", rr); err != nil {
        fmt.Println("Error starting proxy:", err)
    }
}
```

**Checkpoint:**

```bash
mkdir -p /tmp/backend1 /tmp/backend2 /tmp/backend3
echo "Hello from backend 9101" > /tmp/backend1/index.html
echo "Hello from backend 9102" > /tmp/backend2/index.html
echo "Hello from backend 9103" > /tmp/backend3/index.html

python3 -m http.server 9101 --bind 127.0.0.1 --directory /tmp/backend1 &
python3 -m http.server 9102 --bind 127.0.0.1 --directory /tmp/backend2 &
python3 -m http.server 9103 --bind 127.0.0.1 --directory /tmp/backend3 &
```

In another terminal: `go run ./cmd/gobalance`, then `for i in 1 2 3 4 5 6; do curl -s localhost:9090/index.html; done` — you should see the three "Hello from backend ..." lines cycling in exact order.

Then the unit test — this is the part most worth internalizing, not just pasting, since it's the one graded checkpoint in the PRD (§8: *"round robin cycles in exact order"*):

```go
// internal/balancer/round_robin_test.go
package balancer

import (
    "errors"
    "testing"

    "github.com/nicholasanthonys/gobalance/internal/pool"
)

func TestRoundRobin_CyclesInOrder(t *testing.T) {
    p := pool.New([]string{"A", "B", "C"})
    rr := NewRoundRobin(p)

    expectedOrder := []string{"A", "B", "C", "A", "B", "C"}
    for i := 0; i < len(expectedOrder); i++ {
        actual, err := rr.Pick()
        if err != nil {
            t.Fatalf("unexpected error %s", err.Error())
        }
        if actual == nil {
            t.Fatalf("expected a backend, got nil")
        }
        if actual.Addr != expectedOrder[i] {
            t.Errorf("Expected %s, got %s", expectedOrder[i], actual.Addr)
        }
    }
}

func TestRoundRobin_NoHealthyBackends(t *testing.T) {
    p := pool.New([]string{})
    rr := NewRoundRobin(p)

    _, err := rr.Pick()
    if err == nil {
        t.Fatalf("Expected error, got nil")
    }
    if !errors.Is(err, ErrNoHealthyBackends) {
        t.Errorf("Expected ErrNoHealthyBackends, got %s", err.Error())
    }
}
```

Run it: `go test -race ./internal/balancer/...` — both tests should pass.

⚠️ **This exact test file will need a small update once you do Phase 2** — see the note at the top of that section. That's not a mistake here; it's the natural consequence of `Pool.Healthy()` changing behavior.

---

## Phase 2 — Active Health Checks (TCP)

**Goal:** periodically TCP-dial each backend; exclude unhealthy ones from `Pool.Healthy()`.

**Concepts:** hysteresis thresholds, concurrency-safe shared state (THEORY.md §4).

**Before you start:** this phase changes `Pool.Healthy()` from "returns everyone" to "returns only backends a health check has actually verified." That means `TestRoundRobin_CyclesInOrder` from Phase 1 will start failing — a pool built fresh in a test has no `Checker` running, so nothing ever marks its backends healthy. Fix, at the end of this phase: after `pool.New(...)` in that test, call `RecordSuccess(1)` on each backend before asserting on `Pick()`. This is a real, expected consequence of the design getting more correct, not a bug to chase.

### Step 1 — health state on `Backend` (`internal/pool/pool.go`, full file)

This state is written by health-checker goroutines and read by every connection-handling goroutine calling `Pool.Healthy()` — concurrently — so it's guarded by a `sync.Mutex`, not bare fields. New backends start **unhealthy** (`healthy: false`) until the first successful check — this is the deliberate choice made here: never route to a backend that hasn't been verified yet, even at the cost of a brief startup delay before it's eligible.

```go
// internal/pool/pool.go
package pool

import "sync"

type Backend struct {
    Addr             string
    Weight           int
    mu               sync.Mutex
    healthy          bool
    consecutiveFails int
    consecutiveOks   int
}

func (b *Backend) RecordFailure(threshold int) {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.consecutiveFails += 1
    b.consecutiveOks = 0

    if b.consecutiveFails >= threshold {
        b.healthy = false
    }
}

func (b *Backend) RecordSuccess(threshold int) {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.consecutiveOks += 1
    b.consecutiveFails = 0

    if b.consecutiveOks >= threshold {
        b.healthy = true
    }
}

func (b *Backend) IsHealthy() bool {
    b.mu.Lock()
    defer b.mu.Unlock()
    return b.healthy
}

type Pool struct {
    Backends []*Backend
}

func New(addrs []string) *Pool {
    p := &Pool{}
    for _, addr := range addrs {
        p.Backends = append(p.Backends, &Backend{Addr: addr, Weight: 1, healthy: false, consecutiveFails: 0, consecutiveOks: 0})
    }
    return p
}

func (p *Pool) All() []*Backend {
    return p.Backends
}

func (p *Pool) Healthy() []*Backend {
    var healthyBackends []*Backend

    for _, backend := range p.Backends {
        if backend.IsHealthy() {
            healthyBackends = append(healthyBackends, backend)
        }
    }
    return healthyBackends
}
```

Note the `>=` in both `RecordFailure` and `RecordSuccess` — with `threshold=3`, the flip happens exactly on the 3rd consecutive failure/success, matching "unhealthy after N consecutive failed checks" from PRD.md §6.4. Using `>` instead is an easy, subtle off-by-one (flips on the *4th*, not the 3rd) — worth double-checking if you ever rewrite this.

### Step 2 — the checker itself (`internal/healthcheck/tcp.go`, full file)

```go
// internal/healthcheck/tcp.go
package healthcheck

import (
    "context"
    "net"
    "time"

    "github.com/nicholasanthonys/gobalance/internal/pool"
)

type Checker struct {
    Pool           *pool.Pool
    Interval       time.Duration
    Timeout        time.Duration
    UnhealthyTresh int
    HealthyThresh  int
}

func (c *Checker) Run(ctx context.Context) {
    ticker := time.NewTicker(c.Interval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            for _, b := range c.Pool.All() {
                go c.checkOne(b) // fan out; don't let one slow backend delay the rest
            }
        }
    }
}

func (c *Checker) checkOne(b *pool.Backend) {
    conn, err := net.DialTimeout("tcp", b.Addr, c.Timeout)
    if err != nil {
        b.RecordFailure(c.UnhealthyTresh)
        return
    }
    defer conn.Close()
    b.RecordSuccess(c.HealthyThresh)
}
```

`Checker`'s fields are exported here (`Pool`, `Interval`, ...) rather than using a constructor like `RoundRobin` does — both are valid; exported fields let `main.go` build one with a plain struct literal.

### Step 3 — wire it into `main.go`

Construct the `Checker` and start it in its own goroutine **before** calling `proxy.ServeL4` — `ServeL4` blocks forever in its accept loop, so anything placed after that call never runs during normal operation.

```go
// cmd/gobalance/main.go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/nicholasanthonys/gobalance/internal/balancer"
    "github.com/nicholasanthonys/gobalance/internal/healthcheck"
    "github.com/nicholasanthonys/gobalance/internal/pool"
    "github.com/nicholasanthonys/gobalance/internal/proxy"
)

func main() {
    fmt.Println("gobalance starting...")

    p := pool.New([]string{"localhost:9101", "localhost:9102", "localhost:9103"})
    rr := balancer.NewRoundRobin(p)

    checker := &healthcheck.Checker{
        Pool:           p,
        Interval:       5 * time.Second,
        Timeout:        2 * time.Second,
        UnhealthyTresh: 3,
        HealthyThresh:  2,
    }
    go checker.Run(context.Background())

    fmt.Println("L4 listening on :9090")
    if err := proxy.ServeL4("localhost:9090", rr); err != nil {
        fmt.Println("Error starting proxy:", err)
    }
}
```

`context.Background()` is fine for now — real cancellation on shutdown is Phase 8's problem, not this one.

### Step 4 — update the Phase 1 test

```go
// internal/balancer/round_robin_test.go — only the top of TestRoundRobin_CyclesInOrder changes
func TestRoundRobin_CyclesInOrder(t *testing.T) {
    p := pool.New([]string{"A", "B", "C"})
    rr := NewRoundRobin(p)
    p.Backends[0].RecordSuccess(1)
    p.Backends[1].RecordSuccess(1)
    p.Backends[2].RecordSuccess(1)

    expectedOrder := []string{"A", "B", "C", "A", "B", "C"}
    // ... rest of the test is unchanged
}
```

**Checkpoint:** kill one of your three backend processes mid-run (`kill %1` if it's a background job, or Ctrl-C its terminal). Within one health-check interval (5s with the config above), `curl` traffic against both `:9090` (L4) and, once Phase 3 is done, `:9091` (L7) should stop reaching that backend. Restart it and confirm it rejoins after 2 consecutive successful checks. Still missing here, worth adding before moving on: a **unit test for the hysteresis logic itself** — call `RecordFailure`/`RecordSuccess` directly on a `*pool.Backend` in a scripted sequence and assert `IsHealthy()` flips at exactly the right count, with no network involved.

---

## Phase 3 — L7 HTTP Reverse Proxy

**Goal:** an HTTP listener that reverse-proxies to the same backend-pool abstraction, using `httputil.ReverseProxy`.

**Concepts:** L4 vs L7 tradeoffs (THEORY.md §2), proxy headers.

**The tricky part of this phase, worth understanding rather than just copying:** `Rewrite func(*ProxyRequest)` has no return value and no access to the `http.ResponseWriter` — it can only shape the outbound request, it can't write an error response. So when `b.Pick()` fails (no healthy backends), there's no direct way to abort from inside `Rewrite`. The pattern below bridges that gap: stash the error on the request's context, then intercept it in a custom `Transport` *before* any real network call happens, returning it as if `RoundTrip` itself failed — which is exactly the condition `ErrorHandler` listens for.

```go
// internal/proxy/l7.go
package proxy

import (
    "context"
    "net/http"
    "net/http/httputil"
    "net/url"

    "github.com/nicholasanthonys/gobalance/internal/balancer"
)

type pickErrKey struct{}

func NewL7Handler(b balancer.Balancer) http.Handler {
    return &httputil.ReverseProxy{
        Rewrite: func(r *httputil.ProxyRequest) {
            backend, err := b.Pick()
            if err != nil {
                ctx := context.WithValue(r.Out.Context(), pickErrKey{}, err)
                r.Out = r.Out.WithContext(ctx)
                return
            }
            r.SetURL(&url.URL{
                Scheme: "http",
                Host:   backend.Addr,
            })
            r.SetXForwarded() // sets X-Forwarded-For/Host/Proto based on the incoming request
        },
        Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
            if err, ok := req.Context().Value(pickErrKey{}).(error); ok {
                return nil, err // short-circuits before any real network call
            }
            return http.DefaultTransport.RoundTrip(req)
        }),
        ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
            http.Error(w, err.Error(), http.StatusServiceUnavailable)
        },
    }
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
    return f(r)
}
```

A bug worth flagging explicitly because it's easy to miss: `r.SetURL(&url.URL{Host: backend.Addr})` **without** `Scheme: "http"` compiles fine but fails on every single request — `http.Transport` rejects a URL with no scheme (`unsupported protocol scheme ""`). The scheme is not optional.

Wire it in as a second listener in `main.go`, running concurrently with L4 and sharing the same pool and balancer:

```go
// cmd/gobalance/main.go
package main

import (
    "context"
    "fmt"
    "net/http"
    "time"

    "github.com/nicholasanthonys/gobalance/internal/balancer"
    "github.com/nicholasanthonys/gobalance/internal/healthcheck"
    "github.com/nicholasanthonys/gobalance/internal/pool"
    "github.com/nicholasanthonys/gobalance/internal/proxy"
)

func main() {
    fmt.Println("gobalance starting...")

    p := pool.New([]string{"localhost:9101", "localhost:9102", "localhost:9103"})
    rr := balancer.NewRoundRobin(p)

    checker := &healthcheck.Checker{
        Pool:           p,
        Interval:       5 * time.Second,
        Timeout:        2 * time.Second,
        UnhealthyTresh: 3,
        HealthyThresh:  2,
    }
    go checker.Run(context.Background())

    go func() {
        fmt.Println("L7 listening on :9091")
        if err := http.ListenAndServe(":9091", proxy.NewL7Handler(rr)); err != nil {
            fmt.Println("Error starting L7 proxy:", err)
        }
    }()

    fmt.Println("L4 listening on :9090")
    if err := proxy.ServeL4("localhost:9090", rr); err != nil {
        fmt.Println("Error starting L4 proxy:", err)
    }
}
```

This is a simplification for the demo binary — one pool/balancer shared by both listeners. `TECH_STACK.md`'s architecture allows each listener its own independent pool and algorithm; that separation becomes real once config-driven multiple listeners exist (Phase 6), not before.

**Checkpoint:** with the same three labeled backends from Phase 1 running, `curl localhost:9091/index.html` repeatedly — same round-robin cycling as the L4 listener, but now going through a real HTTP client/server round trip with `X-Forwarded-For` set. Confirm it by checking the header at the backend, e.g. swap one dummy backend for `curl -v` output or a tiny script that echoes request headers. Then kill all three backends and confirm `curl localhost:9091` returns a `503` with the `ErrNoHealthyBackends` message in the body, instead of hanging or returning a generic `502`.

---

## Phase 4 — More Algorithms

**Goal:** implement weighted round robin, least connections, weighted least connections, and random/power-of-two-choices — the five algorithms PRD.md §6.3 requires — behind the same `Balancer` interface from Phase 1.

**Concepts:** static vs dynamic algorithms (THEORY.md §3).

**Before `LeastConn` compiles:** it calls `b.ActiveConns()`, which doesn't exist on `Backend` yet — `Weight` (used by weighted round robin below) already does, but live connection counting doesn't. Add it first.

### Step 1 — active-connection tracking on `Backend`

Per `CLAUDE.md`'s stated preference, this is a hot-path counter (incremented/decremented on every single connection), so it's a separate `atomic.Int64`, not folded into the existing mutex-guarded health state:

```go
// internal/pool/pool.go — additions to Backend
type Backend struct {
    Addr             string
    Weight           int
    mu               sync.Mutex
    healthy          bool
    consecutiveFails int
    consecutiveOks   int
    activeConns      atomic.Int64
}

func (b *Backend) IncActiveConns() {
    b.activeConns.Add(1)
}

func (b *Backend) DecActiveConns() {
    b.activeConns.Add(-1)
}

func (b *Backend) ActiveConns() int64 {
    return b.activeConns.Load()
}
```

`Pick()` only *selects* a backend — it has no idea when the resulting connection ends. That lifecycle knowledge lives in the proxy layer, so the increment/decrement pairing goes there, not in the balancer:

```go
// internal/proxy/l4.go — handleConn, right after a successful Pick()
backend, err := b.Pick()
if err != nil {
    return
}
backend.IncActiveConns()
defer backend.DecActiveConns()

upstream, err := net.DialTimeout("tcp", backend.Addr, 5*time.Second)
// ... unchanged from here
```

The `defer` guarantees the decrement happens no matter how `handleConn` exits — dial failure, normal completion, anything — mirroring the existing `defer client.Close()` pattern already in this function.

(L7's `httputil.ReverseProxy` doesn't have as clean a "connection lifecycle" hook as L4's `wg.Wait()` — that wiring is deferred until an L7 listener actually uses `LeastConn`, to avoid adding tracking with no consumer yet.)

### Step 2 — `LeastConn`

```go
// internal/balancer/least_conn.go
package balancer

import "github.com/nicholasanthonys/gobalance/internal/pool"

type LeastConn struct {
    pool *pool.Pool
}

func (l *LeastConn) Pick() (*pool.Backend, error) {
    backends := l.pool.Healthy()
    if len(backends) == 0 {
        return nil, ErrNoHealthyBackends
    }
    best := backends[0]
    for _, b := range backends[1:] {
        if b.ActiveConns() < best.ActiveConns() {
            best = b
        }
    }
    return best, nil
}
```

### Step 3 — weighted round robin

The naive approach — build a list like `[A, A, A, B]` for weights 3:1 and cycle through it — produces *bursty* traffic: three A's back to back, then a gap, then three more. The standard fix is Nginx's **smooth weighted round robin**: each backend has a fixed `weight` and a running `current` total. Every `Pick()`:

1. Add each backend's `weight` to its own `current`.
2. Pick whichever backend now has the highest `current`.
3. Subtract the **total weight of all backends** from the winner's `current`.

This self-corrects — a backend that hasn't won in a while keeps accumulating and eventually must win — which is what interleaves the picks (`A, A, B, A, ...`) instead of clumping them, while still landing on the exact target ratio over time. Worth writing this one yourself once the mechanics click; a statistical test (10,000 picks, tally per backend, assert the ratio is close to the weight ratio within a tolerance) is the right way to verify it, since — unlike plain round robin — there's no single fixed expected sequence to assert against exactly.

### Step 4 — weighted least connections

`LeastConn` picks whoever has the fewest raw `ActiveConns()`. That's unfair once backends have different weights — a weight-2 backend is meant to carry roughly twice the load, so it should be allowed proportionally more connections before it's considered "busier" than a weight-1 backend. The comparison needs to be **connections per unit of weight**, not raw count.

Don't actually divide to get there. `ActiveConns() / Weight` has two problems: integer division truncates (so `1/2` and `3/2` both become `1`, losing real distinctions), and a `Weight` of `0` panics (`runtime error: integer divide by zero` — Go doesn't silently produce `Inf` like some languages). The fix is **cross-multiplication** — comparing `a/b < c/d` by comparing `a*d < c*b` instead, which is algebraically equivalent but pure integer multiplication: no division, no truncation, no divide-by-zero risk.

```go
// internal/balancer/weighted_least_conn.go
package balancer

import "github.com/nicholasanthonys/gobalance/internal/pool"

type WeightedLeastConn struct {
    pool *pool.Pool
}

func NewWeightedLeastConn(p *pool.Pool) *WeightedLeastConn {
    return &WeightedLeastConn{
        pool: p,
    }
}

func (w *WeightedLeastConn) Pick() (*pool.Backend, error) {
    backends := w.pool.Healthy()
    if len(backends) == 0 {
        return nil, ErrNoHealthyBackends
    }

    best := backends[0]
    for _, b := range backends[1:] {
        if int(b.ActiveConns())*best.Weight < int(best.ActiveConns())*b.Weight {
            best = b
        }
    }
    return best, nil
}
```

The `if` condition is `b`'s load-per-weight `<` `best`'s load-per-weight, cross-multiplied: `ActiveConns(b) * Weight(best) < ActiveConns(best) * Weight(b)`. The `int(...)` conversions are needed because `ActiveConns()` returns `int64` but `Weight` is `int` — Go won't mix the two in arithmetic without an explicit conversion.

### Step 5 — random and power-of-two-choices

The fifth algorithm PRD.md §6.3 requires, easy to overlook since none of the earlier phases call it out by name.

**Random** is the simplest possible balancer: no shared state, no accumulator, just pick a uniformly random healthy backend on every call.

```go
// internal/balancer/random.go
package balancer

import (
    "math/rand"

    "github.com/nicholasanthonys/gobalance/internal/pool"
)

type Random struct {
    pool *pool.Pool
}

func NewRandom(p *pool.Pool) *Random {
    return &Random{pool: p}
}

func (r *Random) Pick() (*pool.Backend, error) {
    backends := r.pool.Healthy()
    if len(backends) == 0 {
        return nil, ErrNoHealthyBackends
    }
    return backends[rand.Intn(len(backends))], nil
}
```

Worth knowing: as of Go 1.20, the global `math/rand` source auto-seeds itself — no `rand.Seed(time.Now().UnixNano())` boilerplate needed, unlike older Go code you'll find online.

**Power-of-two-choices** (THEORY.md §3) improves on plain random almost for free: instead of scanning every backend like `LeastConn` does (`O(n)` work per pick, and reading every backend's counter), pick just *two* random healthy backends and route to whichever of those two has fewer `ActiveConns()`. This gets most of `LeastConn`'s load-awareness at a fraction of the cost — real systems reach for this once a pool has hundreds of backends, where scanning all of them on every single request becomes meaningful overhead.

```go
// internal/balancer/power_of_two.go
package balancer

import (
    "math/rand"

    "github.com/nicholasanthonys/gobalance/internal/pool"
)

type PowerOfTwoChoices struct {
    pool *pool.Pool
}

func NewPowerOfTwoChoices(p *pool.Pool) *PowerOfTwoChoices {
    return &PowerOfTwoChoices{pool: p}
}

func (p2c *PowerOfTwoChoices) Pick() (*pool.Backend, error) {
    backends := p2c.pool.Healthy()
    if len(backends) == 0 {
        return nil, ErrNoHealthyBackends
    }
    if len(backends) == 1 {
        return backends[0], nil
    }

    // TODO: pick two distinct random indices into backends.
    // Hint: rand.Intn(len(backends)) for the first index; for the
    // second, rand.Intn(len(backends)-1), then nudge it past the first
    // index if it would otherwise land on or after it. This guarantees
    // two distinct indices with no retry loop needed.
    // TODO: return whichever of the two backends has fewer ActiveConns().

    return nil, nil
}
```

**Checkpoint:** write a test harness with one artificially slow backend (sleep 200ms per request) and confirm least-connections routes proportionally fewer requests to it than round robin would under identical concurrent load — done, see `internal/balancer/least_conn_test.go`: across 300 staggered concurrent requests, `RoundRobin` sent the slow backend an even 100 (1/3, blind to load), `LeastConn` sent it only 4 (noticing the pileup and routing around it). Screenshot or log this distribution for the README.

---

## Phase 5 — HTTP Health Checks + TLS Termination

**Goal:** add HTTP-based active checks (GET a path, expect a status) for L7 pools; add TLS termination to both listener types.

**Concepts:** THEORY.md §4 (why HTTP checks can verify deeper than TCP checks), THEORY.md §6 (termination vs passthrough).

```go
// internal/healthcheck/http.go — checkOne variant
func (c *HTTPChecker) checkOne(b *pool.Backend) {
    ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
    defer cancel()
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+b.Addr+c.path, nil)
    resp, err := c.client.Do(req)
    if err != nil || resp.StatusCode != c.expectedStatus {
        b.RecordFailure(c.unhealthyThresh)
        return
    }
    resp.Body.Close()
    b.RecordSuccess(c.healthyThresh)
}
```

For TLS termination:

```go
// cmd/gobalance/main.go (snippet)
cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
if err != nil { log.Fatal(err) }
tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
ln, err := tls.Listen("tcp", cfg.ListenAddr, tlsCfg)
// for L7: srv := &http.Server{Addr: cfg.ListenAddr, TLSConfig: tlsCfg, Handler: l7Handler}
//         srv.ListenAndServeTLS(cfg.TLS.CertFile, cfg.TLS.KeyFile)
```

Generate a self-signed cert for local testing: `openssl req -x509 -newkey rsa:2048 -nodes -keyout key.pem -out cert.pem -days 365 -subj "/CN=localhost"`.

**Checkpoint:** `curl -k https://localhost:<port>/` succeeds and reaches a backend over plaintext behind the scenes (confirm with a packet capture or just trust the architecture — TLS terminates at GoBalance, backends never see it). HTTP health-check test: point the checker at a backend whose `/healthz` deliberately returns 500, confirm it's excluded.

---

## Phase 6 — Configuration File + Hot Reload

**Goal:** move from hardcoded backend lists to a YAML config, reloadable at runtime.

**Concepts:** THEORY.md §7 (atomic config swap without dropping connections).

```yaml
# configs/example.yaml
listeners:
  - name: web
    type: l7
    listen: ":8080"
    algorithm: least_conn
    health_check:
      path: /healthz
      interval: 5s
      timeout: 2s
      unhealthy_threshold: 3
      healthy_threshold: 2
    backends:
      - addr: "127.0.0.1:9001"
        weight: 1
      - addr: "127.0.0.1:9002"
        weight: 2
```

```go
// internal/config/reload.go
type Store struct {
    current atomic.Pointer[Config]
}

func (s *Store) Get() *Config { return s.current.Load() }

func (s *Store) Reload(path string) error {
    cfg, err := Load(path) // parse + validate
    if err != nil {
        return err // keep serving the old config on invalid input
    }
    s.current.Store(cfg)
    return nil
}
```

Wire `fsnotify` to call `Reload` on file change, and/or a `SIGHUP` handler via `os/signal.Notify`. The key correctness property: every in-flight request/connection was handed a `*Config` (or a derived `*Pool`) at the moment it started, and keeps using that pointer to completion — it never sees a half-swapped state, because `atomic.Pointer.Store` is a single atomic write.

**Checkpoint:** start a long-running `curl` against a slow backend endpoint, and *while it's in flight*, edit the config file to remove that backend and trigger a reload. The in-flight request should complete normally; only the *next* request should route away from the removed backend. This is the demo moment for the PRD's "zero-downtime reload" success metric.

---

## Phase 7 — Observability: Structured Logging + Metrics

**Goal:** JSON logs via `log/slog` for requests, reloads, and health transitions; a `/metrics` endpoint via `prometheus/client_golang` exposing request counts/latency, per-backend active connections and health, and a reload counter.

**Concepts:** THEORY.md §8 (why observability is part of correctness for a proxy, not an add-on). Two different aggregation styles are used here on purpose: push-style counters/histograms (`RequestsTotal`, `RequestDuration`, `ReloadsTotal`) that get incremented at the moment an event happens, vs. a pull-style custom `Collector` for per-backend gauges (`ActiveConns`, `IsHealthy`) that reads state already living on `*pool.Backend` at scrape time instead of duplicating it into a separately-maintained `GaugeVec`. Prefer reading existing state over mirroring it into a second variable that can drift out of sync.

**Dependency:** `go get github.com/prometheus/client_golang/prometheus` (and `.../prometheus/promhttp`) — already on the approved list in CLAUDE.md/TECH_STACK.md §3.

### Step 1 — logger construction (`internal/logging/logging.go`, full file)

One JSON `*slog.Logger`, built once in `main.go` and threaded down to whatever needs to log — no package-level global, so tests can pass their own logger (or `slog.New(slog.DiscardHandler)`) instead of polluting test output.

```go
// internal/logging/logging.go
package logging

import (
    "log/slog"
    "os"
)

func New() *slog.Logger {
    return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
```

### Step 2 — health-transition logging (`internal/healthcheck/tcp.go` + `http.go`)

`RecordFailure`/`RecordSuccess` don't report whether they actually flipped `healthy` — and shouldn't; that's pool-package internal bookkeeping, not a logging concern. The checker already calls `IsHealthy()` elsewhere, so it's the right place to notice a transition: read it before and after the check, log only when it changed.

```go
// internal/healthcheck/tcp.go
type TCPChecker struct {
    Pool           *pool.Pool
    Interval       time.Duration
    Timeout        time.Duration
    UnhealthyTresh int
    HealthyThresh  int
    Logger         *slog.Logger
}

func (c *TCPChecker) checkOne(b *pool.Backend) {
    before := b.IsHealthy()

    conn, err := net.DialTimeout("tcp", b.Addr, c.Timeout)
    if err != nil {
        b.RecordFailure(c.UnhealthyTresh)
    } else {
        conn.Close()
        b.RecordSuccess(c.HealthyThresh)
    }

    if after := b.IsHealthy(); after != before {
        c.Logger.Info("backend health changed", "backend", b.Addr, "healthy", after)
    }
}
```

`HTTPChecker.checkOne` gets the identical `before`/`after` wrapping around its existing dial/status-check logic, plus the same `Logger *slog.Logger` field on the struct.

### Step 3 — counters and histograms (`internal/metrics/metrics.go`, full file)

```go
// internal/metrics/metrics.go
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
    RequestsTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{Name: "gobalance_requests_total", Help: "Total requests handled, by listener and outcome."},
        []string{"listener", "status"},
    )
    RequestDuration = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{Name: "gobalance_request_duration_seconds", Help: "Request/connection duration by listener."},
        []string{"listener"},
    )
    ReloadsTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{Name: "gobalance_reloads_total", Help: "Config reload attempts, by result."},
        []string{"result"}, // "success" or "failure"
    )
)

func init() {
    prometheus.MustRegister(RequestsTotal, RequestDuration, ReloadsTotal)
}
```

`status` for the L4 listener won't be an HTTP status — use something like `"ok"`/`"error"` there, and the real status code (`"200"`, `"503"`, ...) for L7.

### Step 4 — per-backend gauges via a custom `Collector` (`internal/metrics/collector.go`, full file)

A `GaugeVec` needs something to call `.Set()` on it every time active-conn counts or health state change — but those already live on `*pool.Backend` (`ActiveConns()`, `IsHealthy()`), updated by `l4.go`/`l7.go`/the health checkers. Rather than shadow that state, implement `prometheus.Collector` directly and read it live on every scrape:

```go
// internal/metrics/collector.go
package metrics

import (
    "github.com/prometheus/client_golang/prometheus"

    "github.com/nicholasanthonys/gobalance/internal/pool"
)

var (
    activeConnsDesc = prometheus.NewDesc(
        "gobalance_backend_active_connections", "Current active connections per backend.",
        []string{"listener", "backend"}, nil,
    )
    backendHealthyDesc = prometheus.NewDesc(
        "gobalance_backend_healthy", "1 if the backend is currently healthy, else 0.",
        []string{"listener", "backend"}, nil,
    )
)

// BackendCollector reads live state off each listener's pool at scrape
// time instead of caching it. Pools is the same listener-name -> *pool.Pool
// registry main.go already builds at startup; it's read-only after startup,
// so sharing it here needs no extra locking.
type BackendCollector struct {
    Pools map[string]*pool.Pool
}

func (c *BackendCollector) Describe(ch chan<- *prometheus.Desc) {
    ch <- activeConnsDesc
    ch <- backendHealthyDesc
}

func (c *BackendCollector) Collect(ch chan<- prometheus.Metric) {
    for listener, p := range c.Pools {
        for _, b := range p.All() {
            ch <- prometheus.MustNewConstMetric(activeConnsDesc, prometheus.GaugeValue, float64(b.ActiveConns()), listener, b.Addr)
            healthy := 0.0
            if b.IsHealthy() {
                healthy = 1
            }
            ch <- prometheus.MustNewConstMetric(backendHealthyDesc, prometheus.GaugeValue, healthy, listener, b.Addr)
        }
    }
}
```

### Step 5 — wire it into the L4 path (`internal/proxy/l4.go`)

`ServeL4`/`handleConn` need a logger and the listener's name (for metric labels and log lines) threaded through:

```go
func ServeL4(listenAddr string, b balancer.Balancer, tlsConfig *tls.Config, logger *slog.Logger, listenerName string) error {
    // ...unchanged listener setup...
    for {
        conn, err := ln.Accept()
        if err != nil {
            continue
        }
        go handleConn(conn, b, logger, listenerName)
    }
}

func handleConn(client net.Conn, b balancer.Balancer, logger *slog.Logger, listenerName string) {
    start := time.Now()
    defer client.Close()

    backend, err := b.Pick()
    if err != nil {
        metrics.RequestsTotal.WithLabelValues(listenerName, "error").Inc()
        return
    }
    backend.IncActiveConns()
    defer backend.DecActiveConns()

    upstream, err := net.DialTimeout("tcp", backend.Addr, 5*time.Second)
    if err != nil {
        metrics.RequestsTotal.WithLabelValues(listenerName, "error").Inc()
        return
    }
    defer upstream.Close()

    // ...unchanged io.Copy pair + wg.Wait()...

    metrics.RequestsTotal.WithLabelValues(listenerName, "ok").Inc()
    metrics.RequestDuration.WithLabelValues(listenerName).Observe(time.Since(start).Seconds())
    logger.Info("l4 connection served", "listener", listenerName, "backend", backend.Addr, "duration_ms", time.Since(start).Milliseconds())
}
```

### Step 6 — wire it into the L7 path (`internal/proxy/l7.go`)

`ReverseProxy` doesn't expose response status/timing to its `Rewrite` hook, so wrap the returned `http.Handler` in a small middleware that captures both — `ModifyResponse` is the other option, but a wrapping `ResponseWriter` also covers the `Pick()`-failure path where `ErrorHandler` writes the response directly, so it's the one place that sees every outcome:

```go
type statusWriter struct {
    http.ResponseWriter
    status int
}

func (w *statusWriter) WriteHeader(code int) {
    w.status = code
    w.ResponseWriter.WriteHeader(code)
}

func NewL7Handler(b balancer.Balancer, logger *slog.Logger, listenerName string) http.Handler {
    rp := &httputil.ReverseProxy{ /* ...unchanged Rewrite/Transport/ErrorHandler... */ }

    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        sw := &statusWriter{ResponseWriter: w, status: http.StatusOK} // WriteHeader(200) is implicit if never called
        rp.ServeHTTP(sw, r)

        status := strconv.Itoa(sw.status)
        metrics.RequestsTotal.WithLabelValues(listenerName, status).Inc()
        metrics.RequestDuration.WithLabelValues(listenerName).Observe(time.Since(start).Seconds())
        logger.Info("l7 request served", "listener", listenerName, "status", sw.status, "duration_ms", time.Since(start).Milliseconds())
    })
}
```

### Step 7 — reload logging + counter (`cmd/gobalance/main.go`)

The `reload` closure already distinguishes success/failure — that's exactly the `ReloadsTotal` label:

```go
reload := func() {
    if err := store.Reload(*configPath); err != nil {
        metrics.ReloadsTotal.WithLabelValues("failure").Inc()
        logger.Warn("reload failed, keeping old config", "error", err)
        return
    }
    metrics.ReloadsTotal.WithLabelValues("success").Inc()
    logger.Info("reload applied")
    applyConfig(store.Get(), pools, started)
}
```

### Step 8 — assemble in `main.go`

- Build `logger := logging.New()` once, near the top of `main`.
- Pass `logger` into each `TCPChecker`/`HTTPChecker` literal in `startListener`, and pass `logger, l.Name` into `proxy.ServeL4(...)` / `proxy.NewL7Handler(...)`.
- After the `pools` map is built, register the backend collector once: `prometheus.MustRegister(&metrics.BackendCollector{Pools: pools})`.
- Start a metrics server on its own port, in its own goroutine, separate from the L4/L7 listeners — a `-metrics-addr` flag defaulting to `:9100` is enough:
  ```go
  go func() {
      if err := http.ListenAndServe(*metricsAddr, promhttp.Handler()); err != nil {
          logger.Error("metrics server stopped", "error", err)
      }
  }()
  ```

**Checkpoint:** `curl localhost:9100/metrics` shows `gobalance_requests_total`, `gobalance_backend_active_connections`, and `gobalance_backend_healthy` moving as you `curl` the L7 listener and kill/restart a backend. Trigger a reload (`kill -HUP` or edit the config) and confirm `gobalance_reloads_total{result="success"}` increments and a JSON log line appears. Bonus: spin up a local Prometheus + Grafana via docker-compose and build one dashboard panel showing per-backend request rate — a screenshot of this is strong portfolio material.

---

## Phase 8 — Graceful Shutdown

**Goal:** `SIGTERM`/`SIGINT` stops new connections, drains in-flight ones within a timeout, then exits cleanly.

**Concepts:** `http.Server` already has this built in — `Shutdown(ctx)` stops `Accept`ing and waits for in-flight requests, bounded by `ctx`'s deadline. `net.Listener` used directly (L4's raw accept loop) has no such thing, so you build the same shape by hand: close the listener to stop new `Accept`s, and track every in-flight connection in a `sync.WaitGroup` that `Shutdown` waits on. Giving both the same `Shutdown(ctx context.Context) error` method signature means `main` can drain every listener — L4, L7, and the metrics server — the same way, through one small interface, instead of special-casing each type.

### Step 1 — turn the L4 accept loop into a type with a `Shutdown` (`internal/proxy/l4.go`)

The free-standing `ServeL4`/`handleConn` functions become methods on an `L4Server`, so there's a value to call `Shutdown` on:

```go
type L4Server struct {
    Balancer     balancer.Balancer
    Logger       *slog.Logger
    ListenerName string

    mu     sync.Mutex
    ln     net.Listener
    closed bool
    wg     sync.WaitGroup
}

// ListenAndServe opens a TCP (or TLS, if tlsConfig is non-nil) listener on
// addr and runs the accept loop. It blocks until Shutdown closes the
// listener, at which point it returns nil.
func (s *L4Server) ListenAndServe(addr string, tlsConfig *tls.Config) error {
    var ln net.Listener
    var err error
    if tlsConfig != nil {
        ln, err = tls.Listen("tcp", addr, tlsConfig)
    } else {
        ln, err = net.Listen("tcp", addr)
    }
    if err != nil {
        return err
    }
    return s.Serve(ln)
}

// Serve runs the accept loop on an already-open listener. It blocks until
// Shutdown closes the listener, at which point it returns nil.
func (s *L4Server) Serve(ln net.Listener) error {
    s.mu.Lock()
    s.ln = ln
    s.mu.Unlock()

    for {
        conn, err := ln.Accept()
        if err != nil {
            s.mu.Lock()
            closed := s.closed
            s.mu.Unlock()
            if closed {
                return nil // Shutdown closed the listener on purpose
            }
            continue // transient accept error; keep serving
        }
        s.wg.Add(1)
        go func() {
            defer s.wg.Done()
            s.handleConn(conn)
        }()
    }
}
```

`Serve` takes a `net.Listener` rather than an address (mirroring `http.Server.Serve`/`ListenAndServe`) so tests — and anything else that needs the bound address before serving starts, e.g. an ephemeral `:0` port — can create the listener themselves.

### Step 2 — `Shutdown` and the connection handler (`internal/proxy/l4.go`)

```go
// Shutdown stops accepting new connections and waits for in-flight ones to
// finish their io.Copy pumps, up to ctx's deadline.
func (s *L4Server) Shutdown(ctx context.Context) error {
    s.mu.Lock()
    s.closed = true
    ln := s.ln
    s.mu.Unlock()
    if ln != nil {
        ln.Close()
    }

    done := make(chan struct{})
    go func() {
        s.wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}

func (s *L4Server) handleConn(client net.Conn) {
    // ...unchanged body from Phase 7, just reading off s.Balancer/s.Logger/
    // s.ListenerName instead of function parameters...
}
```

The `closed` flag is what tells `Serve` an `Accept` error was *caused by* `Shutdown` (so it should return cleanly) rather than a real transient error (so it should keep looping) — closing a listener that's blocked in `Accept` is exactly how you interrupt it in Go; there's no separate cancellation mechanism.

### Step 3 — give L7 and the metrics server the same shape (`cmd/gobalance/main.go`)

Both already run on `net/http`, so instead of calling `http.ListenAndServe(...)` directly (which gives you nothing back to shut down later), construct an `*http.Server` value and keep it:

```go
handler := proxy.NewL7Handler(bal, logger, l.Name)
srv := &http.Server{Addr: l.Listen, Handler: handler}
go func() {
    var err error
    if tlsConfig != nil {
        srv.TLSConfig = tlsConfig
        err = srv.ListenAndServeTLS("certs/cert.pem", "certs/key.pem")
    } else {
        err = srv.ListenAndServe()
    }
    if err != nil && err != http.ErrServerClosed {
        logger.Error("l7 listener stopped", "listener", l.Name, "error", err)
    }
}()
```

`http.ErrServerClosed` is the expected return value once `Shutdown` runs — checking for it keeps a clean shutdown from being logged as an error. Do the same for the metrics server:

```go
metricsSrv := &http.Server{Addr: *metricsAddr, Handler: promhttp.Handler()}
go func() {
    if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
        logger.Error("metrics server stopped", "error", err)
    }
}()
```

### Step 4 — one interface to shut them all down (`cmd/gobalance/main.go`)

```go
// shutdownableServer is satisfied by both *http.Server (L7 listeners, the
// metrics server) and *proxy.L4Server (L4 listeners) so main can drain every
// listener the same way on SIGTERM/SIGINT.
type shutdownableServer interface {
    Shutdown(ctx context.Context) error
}
```

`startListener` changes from blocking (it used to *be* the accept loop) to starting the server in a background goroutine and returning immediately with a handle:

```go
func startListener(l config.Listener, p *pool.Pool, tlsConfig *tls.Config, logger *slog.Logger) (shutdownableServer, error) {
    bal, err := balancer.New(l.Algorithm, p)
    if err != nil {
        return nil, fmt.Errorf("listener %q: %w", l.Name, err)
    }

    switch l.Type {
    case "l4":
        // ...build the TCPChecker as in Phase 7...
        srv := &proxy.L4Server{Balancer: bal, Logger: logger, ListenerName: l.Name}
        go func() {
            if err := srv.ListenAndServe(l.Listen, tlsConfig); err != nil {
                logger.Error("l4 listener stopped", "listener", l.Name, "error", err)
            }
        }()
        return srv, nil

    case "l7":
        // ...build the HTTPChecker as in Phase 7, then the *http.Server from Step 3...
        return srv, nil

    default:
        return nil, fmt.Errorf("listener %q: unknown type %q", l.Name, l.Type)
    }
}
```

Only startup errors (bad algorithm, unknown listener type) come back synchronously now — an error once the server is already serving is logged from inside its goroutine instead, same as before.

### Step 5 — collect the handles and wire up the signal (`cmd/gobalance/main.go`)

Where the listener-starting loop used to fire-and-forget a goroutine per listener, collect what `startListener` now returns:

```go
var servers []shutdownableServer
for _, l := range store.Get().Listeners {
    p := pools[l.Name]
    srv, err := startListener(l, p, tlsConfig, logger)
    if err != nil {
        fmt.Printf("listener %q failed to start: %v\n", l.Name, err)
        continue
    }
    servers = append(servers, srv)
}
// ...build metricsSrv as in Step 3, then:
servers = append(servers, metricsSrv)
```

Add a `-shutdown-timeout` flag next to the other flags (default `30*time.Second` is a reasonable starting point), then replace the `select {}` that used to keep `main` alive with an actual shutdown sequence:

```go
shutdownCh := make(chan os.Signal, 1)
signal.Notify(shutdownCh, syscall.SIGTERM, syscall.SIGINT)
<-shutdownCh
logger.Info("shutdown signal received, draining", "timeout", shutdownTimeout.String())

ctx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
defer cancel()

var wg sync.WaitGroup
for _, srv := range servers {
    wg.Add(1)
    go func() {
        defer wg.Done()
        if err := srv.Shutdown(ctx); err != nil {
            logger.Warn("listener did not shut down cleanly", "error", err)
        }
    }()
}
wg.Wait()
logger.Info("shutdown complete")
```

Shutting every listener down concurrently (rather than one after another) matters here: with a single shared `ctx` deadline, sequential shutdowns would let an earlier slow listener eat into the time budget of a later one.

**Checkpoint:** start a slow request (a backend that sleeps a few seconds before responding), send `SIGTERM` to GoBalance immediately after (`kill -TERM $(pgrep -f gobalance)` or `Ctrl+C`), and confirm the in-flight request still completes successfully while a new connection attempt during the drain window is refused rather than accepted. `internal/proxy/l4_test.go`'s `TestL4Server_ShutdownDrainsInFlightConnections` automates exactly this scenario for the L4 path — worth reading even if you don't write it yourself, since it's the clearest proof that `Shutdown` is actually blocking on the `WaitGroup` and not just closing the listener and returning.

---

## Phase 9 — Race Audit + Load Testing

**Goal:** prove concurrency correctness and gather real performance numbers.

```bash
go build -race -o gobalance-race ./cmd/gobalance
go test -race ./...
```

Run every integration test and a manual soak (a few minutes of `vegeta attack` against the race-instrumented binary) — the race detector only catches races on code paths actually exercised, so exercise all of them: concurrent config reloads during load, backends flapping during load, TLS and plaintext simultaneously if both are running.

```bash
echo "GET http://localhost:8080/" | vegeta attack -duration=30s -rate=2000 | vegeta report
```

Record throughput, p50/p95/p99 latency, and error rate. Then re-run while killing a backend container partway through, and capture the error-rate blip (should be small and short-lived) for the README.

**Checkpoint:** `go test -race ./...` is clean; you have a load-test report with real numbers backing the PRD's performance targets, plus a "failure injection" run showing bounded, brief impact.

---

## Phase 10 — Containerize and Package for Demo

**Goal:** one-command demo via Docker Compose.

```dockerfile
# deploy/Dockerfile
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /gobalance ./cmd/gobalance

FROM gcr.io/distroless/static
COPY --from=build /gobalance /gobalance
COPY configs/example.yaml /configs/example.yaml
ENTRYPOINT ["/gobalance", "-config=/configs/example.yaml"]
```

```yaml
# deploy/docker-compose.yaml
services:
  gobalance:
    build: { context: .., dockerfile: deploy/Dockerfile }
    ports: ["8080:8080", "9090:9090"]
    depends_on: [backend1, backend2, backend3]
  backend1:
    image: hashicorp/http-echo
    command: ["-text=hello from backend-1"]
  backend2:
    image: hashicorp/http-echo
    command: ["-text=hello from backend-2"]
  backend3:
    image: hashicorp/http-echo
    command: ["-text=hello from backend-3"]
```

**Checkpoint:** `docker compose -f deploy/docker-compose.yaml up`, then in another terminal `for i in {1..9}; do curl -s localhost:8080; done` shows requests cycling across all three backends. `docker kill <backend container>` shows failover live in the logs.

---

## Phase 11 — Polish for Portfolio

Write a README with: a one-paragraph pitch, the architecture diagram from TECH_STACK.md, the quick-start Docker Compose command, a GIF or short recording of the failover demo, a link to this tutorial and the theory doc, and the load-test numbers from Phase 9. Tag a `v1.0` release once M1–M8 in the PRD are all checked off.

If you want to keep going, the PRD's "Future Work" section (service discovery, sticky sessions, rate limiting, an admin UI) is a ready-made list of v2 ideas — each one is its own small, well-scoped follow-up project rather than scope creep on this one.
