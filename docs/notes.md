# Notes

Running notes on concepts and design decisions worth remembering — not formal design docs (see `PRD.md`/`TECH_STACK.md`/`THEORY.md` for those), just things worth writing down so they don't get lost.

## Pure state vs. coupling (health-check thresholds)

Came up deciding whether `Backend.RecordFailure`/`RecordSuccess` should take a `threshold int` parameter on every call (current design), or whether the threshold should be stored as a field on `Backend`/`Pool` instead.

**Pure state** — a struct that holds *facts* but has no opinion on the *rules* governing those facts. `internal/pool/pool.go`'s `Backend` is an example:

```go
type Backend struct {
    Addr             string
    Weight           int
    mu               sync.Mutex
    healthy          bool
    consecutiveFails int
    consecutiveOks   int
}
```

It knows how many consecutive fails have happened and its current health flag, but it has zero opinion on *when* that flag should flip — that decision ("3 fails flips it") lives entirely in whoever calls `RecordFailure(3)`. `Backend` just executes whatever rule it's handed, one call at a time.

**Coupling** — how much a change in one piece of code forces a change in another, unrelated piece. Low coupling = parts change independently. High coupling = a change in A ripples into B even though B's own responsibility didn't change.

Applied to this decision: with thresholds living on `internal/healthcheck/tcp.go`'s `Checker` (current design), tuning a threshold or adding a second checker with a *different* threshold (e.g. an HTTP checker alongside the TCP one, or the passive checks `PRD.md` §6.4 describes) touches only `Checker` — `Backend`/`Pool` are untouched. If threshold were stored on `Backend` instead, that same change would force editing `Backend`'s definition too (a second field, a way to pick which threshold applies), because one stored value can't represent two different callers' policies. A change that's conceptually about *health-check policy* would end up requiring edits to *state storage* — that ripple is coupling made concrete.

**Decision:** kept threshold as a parameter. Costs one extra argument at the single call site today (`checkOne` in `tcp.go`); avoids re-touching `Backend` every time the health-check policy evolves — which is expected, given passive checks are still on the roadmap.

## Why `ActiveConns` is atomic, and why `Pick()` doesn't own its lifecycle

Came up implementing `LeastConn`, which needs to know each backend's *current* in-flight connection count.

**Atomic, not mutex-guarded.** `CLAUDE.md` calls this out explicitly: hot-path counters (connection counts, round-robin cursors) should prefer `atomic.Int64`/`atomic.Uint64`; `sync.Mutex` is for less-hot, more-structural state. `ActiveConns` gets touched on *every single connection*, so it's a separate `atomic.Int64` field on `Backend`, not folded into the existing `mu`-guarded health state (which only changes on health-check ticks — much colder).

**`Pick()` can only select, not track a lifecycle.** A balancer's `Pick()` method returns a backend and returns immediately — it has no way to know when the resulting connection actually *ends*. That knowledge only exists in the proxy layer, which owns the connection's full lifetime (`l4.go`'s `handleConn`, via `wg.Wait()`). So the increment happens right after `Pick()` succeeds, and the decrement is `defer`red in the same function — mirroring the existing `defer client.Close()` pattern already there. The lesson generalizes: **whichever piece of code owns a resource's start and end is the only piece of code that can safely track its lifecycle.** A balancer only owns the *decision*, not the *duration*.

## Smooth weighted round robin — why not just repeat backends in a list

Naive weighting: for weights A=3, B=1, cycle through the list `[A, A, A, B]`. This technically gives the right 3:1 ratio, but the *order* is bursty — three A's land back to back, then a gap, then three more. A real backend would rather see a steady trickle than periodic bursts.

**Nginx's smooth weighted round robin** fixes the ordering while keeping the ratio exact. Each backend has a fixed `weight` and a running `current` accumulator. Every `Pick()`, for *every* backend: add its `weight` to `current`; whichever backend now has the highest `current` wins; subtract the **total weight of all backends** from the winner's `current`. This self-corrects — a backend that hasn't won recently keeps accumulating and is eventually forced to win — which is what interleaves picks (`A, A, B, A, ...`) instead of clumping them.

Traced by hand, A(weight=3) vs B(weight=1), total=4:

| Pick | current before | after +weight | winner | after −total (4) from winner |
|---|---|---|---|---|
| 1 | A=0, B=0 | A=3, B=1 | A | A=-1, B=1 |
| 2 | A=-1, B=1 | A=2, B=2 | A (tie→first) | A=-2, B=2 |
| 3 | A=-2, B=2 | A=1, B=3 | B | A=1, B=-1 |
| 4 | A=1, B=-1 | A=4, B=0 | A | A=0, B=0 |

Sequence: `A, A, B, A` — interleaved, not clumped, while still landing on 3:1 over time.

**Testing implication:** unlike plain round robin, there's no single fixed sequence to assert against exactly — the order depends on how all the weights interact. The right test is statistical: run `Pick()` many times (e.g. 10,000), tally wins per backend, assert the ratio is close to the weight ratio within a tolerance.

**Where the accumulator state lives — same "pure state vs. coupling" idea as above.** It's tempting to add `current int` as a field on `Backend`, next to `Weight`. Don't — `current` is bookkeeping private to *this one algorithm's* math, not a fact about the backend (unlike `Weight`, a real property, or `ActiveConns`, which every algorithm might care about). It belongs on `WeightedRoundRobin` itself (e.g. `map[*pool.Backend]int`, guarded by its own mutex) — same instinct as `RoundRobin` keeping its `next` cursor private to itself. If `Backend` accumulated a field for every algorithm's internal bookkeeping, it would become exactly the kind of dumping ground that couples unrelated parts of the system together.

## Comparing the four algorithms

All four implement the same `Balancer` interface (`Pick() (*pool.Backend, error)`), but they look at fundamentally different information to make that decision:

| Algorithm | What it looks at | Static or dynamic | File |
|---|---|---|---|
| `RoundRobin` | Nothing — just cycles in fixed order | Static: every backend is treated as identical | `round_robin.go` |
| `WeightedRoundRobin` | Configured `Weight` only | Static: the ratio never changes at runtime, regardless of load | `weighted_round_robin.go` |
| `LeastConn` | Live `ActiveConns()` only | Dynamic: reacts to real-time load | `least_conn.go` |
| `WeightedLeastConn` | Live `ActiveConns()` **and** configured `Weight` | Dynamic, capacity-aware | `weighted_least_conn.go` |

"Static vs. dynamic" (THEORY.md §3) is the key distinction: static algorithms decide purely from configuration and don't care what's actually happening to each backend right now; dynamic algorithms watch live state and adapt.

### Worked example: 3 identical backends, one goes slow

Say A, B, C all have `Weight: 1`, and normally respond in 10ms. Then C starts taking 500ms per request (overloaded, or a slow dependency) — but it's still passing health checks (it's slow, not down, so `IsHealthy()` stays `true`).

- **`RoundRobin`** doesn't know or care. It keeps sending exactly 1/3 of traffic to C, same as before. Every third request now waits 500ms — C is actively dragging down overall latency, and the algorithm has no way to notice.
- **`WeightedRoundRobin`** (all weights equal) behaves identically to plain `RoundRobin` here — weight is fixed at config time, so a runtime slowdown doesn't change anything. Weighting only helps if you *already knew* C had less capacity and configured it with a lower weight in advance.
- **`LeastConn`** notices without being told. Because C is slow, requests routed to it pile up as `ActiveConns()` — each one takes 500ms to finish and decrement the counter, while A and B's requests finish and decrement almost instantly. C's connection count climbs relative to A and B's, so `Pick()` naturally starts favoring A and B. No config change needed; this is exactly the "reacts to real-time load" behavior static algorithms can't do.
- **`WeightedLeastConn`** does the same thing, but would also respect a configured capacity difference on top of it — e.g. if C were *supposed* to have half the capacity of A (a smaller instance type, say), `WeightedLeastConn` accounts for both facts at once: C's configured smaller capacity *and* its live slowdown. `LeastConn` alone would only see the slowdown, not the intended capacity difference.

### Worked example: 2 backends with different declared capacity, both fast

Say A has `Weight: 3`, B has `Weight: 1` (A is meant to handle 3x B's traffic — bigger instance, more cores, whatever the real-world reason), and both currently respond quickly with no real load.

- **`RoundRobin`** ignores weight entirely — A and B each get exactly half the traffic, even though A was provisioned for 3x that share. Wastes A's extra capacity.
- **`WeightedRoundRobin`** gets this right: over any window, A gets ~75% of picks, B gets ~25%, smoothly interleaved (not bursty — see the SWRR note above).
- **`LeastConn`** also gets this wrong in a subtler way: with no real load differentiating them, `ActiveConns()` for A and B will be roughly equal most of the time, so it behaves close to round robin here — again splitting roughly 50/50, ignoring that A was meant to take more.
- **`WeightedLeastConn`** is the one that gets both facts right simultaneously: at rest (no real load pressure), it still respects the 3:1 weight ratio, and if real load pressure shows up unevenly later, it adapts to that too.

**Takeaway:** static algorithms (`RoundRobin`, `WeightedRoundRobin`) encode what you *expect*; dynamic algorithms (`LeastConn`, `WeightedLeastConn`) react to what's *actually happening*. The weighted variants of each add "respect configured capacity" on top of their base behavior. `WeightedLeastConn` is the only one of the four that does both — which is also why it's the most complex to implement and reason about.

### Where `LeastConn` and `WeightedLeastConn` actually disagree

The two can pick *different* backends for the identical situation — worth a concrete numeric case, not just the general description above.

Two backends: **X** is a bigger instance, `Weight: 3`, currently serving **2** active connections. **Y** is a normal instance, `Weight: 1`, currently serving **1** active connection.

- **`LeastConn`** only compares raw `ActiveConns()`: `2` (X) vs `1` (Y). Y has fewer, so **`LeastConn` picks Y**. It never reads `Weight` at all.
- **`WeightedLeastConn`** compares load *relative to capacity*: X is at `2/3 ≈ 0.67` of its budget, Y is at `1/1 = 1.0` of its budget. X has more headroom despite having more raw connections, so **`WeightedLeastConn` picks X**.

Verified against the actual cross-multiplication code in `weighted_least_conn.go` — with `best = Y`, `b = X`: is `ActiveConns(X)*Weight(Y) < ActiveConns(Y)*Weight(X)`? → `2*1 < 1*3` → `2 < 3` → true → X wins. Matches the ratio reasoning.

They only ever agree when all weights are equal (which is why a same-`Weight` test, like `least_conn_test.go`'s slow-backend checkpoint, doesn't need to distinguish them). The moment weights differ, "fewer raw connections" and "less loaded relative to capacity" can point at different backends — `LeastConn` treats every backend as the same size; `WeightedLeastConn` doesn't.

## Power-of-two-choices: picking two distinct random indices without a retry loop

Came up implementing `PowerOfTwoChoices.Pick()` (`internal/balancer/power_of_two.go`), which needs two *different* random backends to compare — if both random picks could land on the same index, you're not really comparing two candidates, just picking one and comparing it to itself.

**The naive fix** — keep re-rolling the second index until it differs from the first — works but is an unbounded loop for no real reason. **The trick that avoids the loop entirely:**

```go
firstIdx := rand.Intn(len(backends))
secondIdx := rand.Intn(len(backends) - 1)
if secondIdx >= firstIdx {
    secondIdx++
}
```

Idea: draw the second index from a range one shorter than the full backend list (`len(backends)-1` possible values — exactly as many as there are *remaining* indices once `firstIdx` is excluded), then shift it past `firstIdx` if needed so it lands on one of those remaining indices instead of skipping over `firstIdx`.

**Why the condition has to be `>=`, not `==`.** Traced with 5 backends, `firstIdx = 2`, raw `secondIdx` drawn from `{0,1,2,3}`:

| raw secondIdx | with `>=` (correct) | with `==` (buggy) |
|---|---|---|
| 0 | 0 | 0 |
| 1 | 1 | 1 |
| 2 | 3 | 3 |
| 3 | 4 | 3 |

With `>=`, the four raw values map onto `{0,1,3,4}` — exactly the four valid remaining indices, each hit once (a clean bijection). With `==`, only the *exact* collision (raw `2`) gets nudged; raw `3` doesn't equal `firstIdx` so it's left alone, but it was already supposed to land on `4` after the shift. Result: index `3` gets hit twice as often as it should, and index `4` is never reachable at all — a silent, statistical bias rather than a crash. The general principle: excluding one value from a range doesn't just require patching the single number that collides with it — every value *after* the excluded one has to shift to fill the gap, like closing a gap in a number line. `==` only patches the collision point; `>=` shifts the whole tail.

## `-tls=false` gotcha — your test client has to match the mode you ran the server in

`cmd/gobalance/main.go` has a `-tls` flag (default `true`) that picks between `tls.Listen`/`http.ListenAndServeTLS` and plain `net.Listen`/`http.ListenAndServe`. Ran it once with `-tls=false` to test the plaintext path, then reused the same TLS-probing commands from before it — got this from `openssl s_client`:

```
error:0A00010B:SSL routines:ssl3_get_record:wrong version number
```

and this from `curl -k https://...`:

```
curl: (35) OpenSSL/3.0.13: error:0A00010B:SSL routines::wrong version number
```

**Not a bug in the toggle.** Both `curl -k https://...` and `openssl s_client` unconditionally send a TLS ClientHello the instant the TCP connection opens — there's no "try plaintext" fallback in either tool. Point one at a listener that's actually speaking plain HTTP/TCP, and the first few bytes back get read as a TLS record, fail to parse as one, and OpenSSL reports "wrong version number." The error is coming from the version field of what OpenSSL *assumed* was a TLS record header, not an actual TLS version mismatch.

The fix isn't in the code — it's using the client that matches the mode the server is actually running in:

| Server mode | L7 check | L4 check |
|---|---|---|
| `-tls=true` (default) | `curl -k https://localhost:9091/` | `openssl s_client -connect localhost:9090` |
| `-tls=false` | `curl http://localhost:9091/` | `printf 'ping' \| nc localhost 9090` (`openssl s_client` always speaks TLS, so it can't test a plaintext listener at all — use `nc` or plain `curl` instead) |

General takeaway: "wrong version number" / "unknown protocol" from an OpenSSL-based tool almost always means *the two ends disagree about whether TLS is happening at all*, not a cipher/version negotiation failure between two TLS peers — worth remembering since the error text reads like the latter.
