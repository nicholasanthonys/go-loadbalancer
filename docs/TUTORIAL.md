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
| 4 — More Algorithms | In progress | `ActiveConns` tracking wired into `l4.go`; `RoundRobin`, `LeastConn`, `WeightedRoundRobin`, `WeightedLeastConn` all implemented and tested (statistical test, cross-multiplication comparison, slow-backend checkpoint — RoundRobin 100/300 to the slow backend vs LeastConn's 4/300). **Missing:** `Random` and `PowerOfTwoChoices` — the 5th algorithm PRD.md §6.3 requires, not called out in earlier drafts of this doc. |
| 5 — HTTP Health Checks + TLS Termination | Not started | |
| 6 — Configuration File + Hot Reload | Not started | `internal/config/` is empty; `configs/` has no files. |
| 7 — Observability: Logging + Metrics | Not started | `internal/metrics/`, `internal/logging/` are empty. |
| 8 — Graceful Shutdown | Not started | |
| 9 — Race Audit + Load Testing | Not started | |
| 10 — Containerize and Package for Demo | Not started | `deploy/` is empty. |
| 11 — Polish for Portfolio | Not started | |

**Pick up here:** Phase 4, Step 5 — implement `Random` and `PowerOfTwoChoices` in `internal/balancer`, the last algorithm PRD.md §6.3 requires.

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

**Goal:** JSON logs via `log/slog`; a `/metrics` endpoint via `prometheus/client_golang`.

```go
// internal/logging/logging.go
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
logger.Info("backend health changed", "backend", b.Addr, "healthy", b.IsHealthy())
```

```go
// internal/metrics/metrics.go
var (
    RequestsTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{Name: "gobalance_requests_total"},
        []string{"listener", "backend", "status"},
    )
    RequestDuration = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{Name: "gobalance_request_duration_seconds"},
        []string{"listener"},
    )
    BackendHealthy = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{Name: "gobalance_backend_healthy"},
        []string{"backend"},
    )
)
// register all in an init() or explicit Register(), then:
http.Handle("/metrics", promhttp.Handler())
```

Increment `RequestsTotal` and observe `RequestDuration` from the L7 handler's `ModifyResponse`/wrapped `ResponseWriter`; set `BackendHealthy` from the health checker on every state transition.

**Checkpoint:** `curl localhost:<metrics-port>/metrics` shows real counters moving as you generate traffic. Bonus: spin up a local Prometheus + Grafana via docker-compose and build one dashboard panel showing per-backend request rate — a screenshot of this is strong portfolio material.

---

## Phase 8 — Graceful Shutdown

**Goal:** `SIGTERM`/`SIGINT` stops new connections, drains in-flight ones within a timeout, then exits cleanly.

```go
// cmd/gobalance/main.go (snippet)
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
<-sigCh
logger.Info("shutdown signal received, draining")

ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
srv.Shutdown(ctx) // L7: stdlib handles draining for you

// L4: close the listener, then wait on a WaitGroup tracking active handleConn goroutines
l4Listener.Close()
waitWithTimeout(activeConnsWG, 30*time.Second)
```

**Checkpoint:** start a slow request (backend sleeps 5s), send `SIGTERM` to GoBalance immediately after, and confirm the in-flight request still completes successfully while new connection attempts during the drain window are refused/queued rather than accepted normally.

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
