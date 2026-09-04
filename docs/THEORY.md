# Load Balancing Theory

This document explains the concepts behind GoBalance, independent of the Go implementation. Read this before or alongside the tutorial — the tutorial tells you what to type, this explains why it works.

## 1. What a Load Balancer Actually Does

At its core, a load balancer sits between clients and a set of servers and does three things: it accepts a connection or request, it decides which backend server should handle it, and it forwards traffic there (and back). Everything else — health checks, algorithms, TLS, metrics — exists to make that decision and forwarding step correct and observable under real-world failure conditions.

The reason this is hard is not the forwarding. Copying bytes from one socket to another is a few lines of code. The hard parts are: making the routing decision fast under concurrent load, knowing which backends are actually healthy without either false-positive routing to dead servers or false-negative flapping, and doing all of this without ever becoming the single slowest or least reliable part of the system it's supposed to be protecting.

## 2. Layer 4 vs Layer 7

These names come from the OSI model, and the distinction matters because it determines what information the load balancer has when it makes a decision.

**Layer 4 (transport layer)** load balancing operates on TCP (or UDP) connections. The LB sees IP addresses and ports, establishes a connection to a backend, and then largely gets out of the way — it forwards raw bytes in both directions without looking inside them. It doesn't know or care if the payload is HTTP, gRPC, a database protocol, or something custom. This makes it fast (minimal per-byte processing) and protocol-agnostic, but it also means routing decisions can only be made once, at connection time — the LB can't look at an HTTP path or header to decide where a *specific request* on an already-established connection should go, because at L4 it doesn't parse requests at all.

**Layer 7 (application layer)** load balancing operates on the actual application protocol — almost always HTTP. The LB terminates the incoming connection, parses the request, and can make routing decisions based on anything in it: the URL path, a header, a cookie, the method. It then makes a *separate* connection (or reuses a pooled one) to the chosen backend and issues the request there. This is more expensive per request (full HTTP parsing, potentially two separate TCP connections instead of one relayed pipe) but far more flexible — this is what makes path-based routing, sticky sessions via cookies, and request-level retries possible.

A useful mental model: L4 is a smart cable — it decides once, at connect time, which two endpoints to wire together, then steps back. L7 is a receptionist — it reads every request that comes in and decides, per request, where it goes, potentially even rewriting it along the way.

GoBalance implements both because the tradeoff is worth demonstrating directly: the L4 path is simpler and shows the raw socket-forwarding mechanics, while the L7 path built on `httputil.ReverseProxy` shows request-aware routing and header manipulation.

## 3. Load-Balancing Algorithms

All algorithms answer the same question — "given a set of healthy backends, which one gets this connection/request?" — with different tradeoffs between simplicity, fairness, and adaptiveness to real backend load.

**Round robin.** Cycle through the backend list in order, wrapping around. Dead simple, O(1) per pick, and fair *by count* — every backend gets an equal share of requests over time. Its weakness: it assumes every request costs roughly the same amount of backend work and every backend has equal capacity. Neither is usually true.

**Weighted round robin.** Same idea, but each backend has an integer weight, and higher-weight backends appear more often in the rotation (e.g. weights 3:1 mean backend A gets 3 requests for every 1 to backend B). This fixes the "unequal capacity" problem — a bigger instance can be given a proportionally larger share — but still assumes uniform request cost.

**Least connections.** Track the number of currently-active connections/requests per backend and route each new one to whichever backend has the fewest. This adapts to reality in a way round robin can't: if one backend is slow and requests pile up on it, least-connections naturally routes new traffic away from it, because "active connections" is a live proxy for "how backed up is this server right now." The cost is needing shared, concurrently-updated counters — every connection open and close has to atomically adjust state that every routing decision reads.

**Weighted least connections.** Least connections, but the comparison is normalized by weight (e.g. `active_connections / weight`), so a backend rated for twice the capacity is expected to carry roughly twice the active connections before it's considered equally loaded.

**Random / power of two choices.** Picking a backend uniformly at random is a surprisingly reasonable baseline — over enough requests it converges to a fair distribution with zero shared state and zero coordination overhead, which matters at extreme scale. "Power of two choices" improves on pure random: pick two backends at random and route to whichever has fewer active connections. This gets most of the load-awareness of least-connections with far less contention on shared state, because you only need to read (not lock) a couple of counters instead of scanning or serializing access to all of them.

**IP hash / consistent hashing** (mentioned for completeness, not required in v1): hash the client IP (or another key) to deterministically map a client to the same backend across requests. This buys session affinity without cookies, at the cost of uneven distribution if client IPs aren't uniformly distributed, and painful rebalancing when the backend set changes — which is exactly the problem consistent hashing (as opposed to plain modulo hashing) is designed to minimize.

The general pattern worth internalizing: static algorithms (round robin, weighted RR) are simple and predictable but blind to real-time backend state; dynamic algorithms (least connections and its variants) react to actual load but require synchronized shared state, which is itself a concurrency problem to get right.

## 4. Health Checking

A load balancer that doesn't know a backend is dead is worse than no load balancer — it actively routes traffic into a black hole. Health checking exists to keep the LB's view of the world accurate.

**Active health checks** are probes the LB initiates on a timer, independent of real traffic: a TCP connect attempt (does the port accept connections?) or an HTTP request to a designated path like `/healthz` with an expected status code. Active checks catch failures even when a backend is receiving no live traffic, and they can check deeper than "is the socket open" — an HTTP healthcheck endpoint can verify the app can reach its own database, for instance.

**Passive health checks** observe real traffic instead of generating synthetic probes: if requests routed to a backend start timing out or returning connection errors, the LB can eject it immediately without waiting for the next active-check interval. Passive checks catch failures faster (no polling delay) but only work once traffic is already flowing there, so they're a complement to active checks, not a replacement.

**Hysteresis (thresholds) matters more than it looks.** A single failed check is not enough evidence a backend is actually down — it could be a transient network blip, a GC pause, or a health-check timeout that was set too aggressively. Requiring N consecutive failures before marking a backend unhealthy avoids "flapping" it out of rotation on noise. Symmetrically, requiring M consecutive successes before marking it healthy again avoids flapping it back in while it's still recovering (e.g. a server that just restarted and is still warming caches). Real-world systems (Envoy, HAProxy, most cloud LBs) all use some form of this unhealthy-threshold / healthy-threshold pair for exactly this reason — it's not an implementation detail, it's a core correctness property.

**Health-check cost is a real tradeoff.** Checking every backend every second is more responsive but adds constant load; checking every 30 seconds is cheaper but means a dead backend can receive traffic for up to 30 seconds before ejection. The interval, plus the failure threshold, together determine your worst-case "time to detect," which is a number worth being able to state explicitly for any health-checked system you build or operate.

## 5. Connection Handling Models

Historically there have been two dominant models for handling many concurrent connections: **thread-per-connection** (simple to write, but OS threads are expensive — megabytes of stack each, expensive context switches — so this caps out at thousands, not millions, of concurrent connections) and **event-driven / single-threaded async** (nginx's original model, Node.js) where one thread handles many connections via non-blocking I/O and an event loop, which scales far better but forces the entire codebase into callback- or async-await-style non-blocking code, which is harder to reason about.

Go's goroutine model is a third option that gets most of the benefit of both: you write **blocking-style code** (`conn.Read()` just blocks, like thread-per-connection code) but the Go runtime's scheduler multiplexes many goroutines onto a small pool of OS threads (the "M:N" model — M goroutines on N OS threads), parking a goroutine when it blocks on I/O and running something else on that thread in the meantime. Goroutines start with a tiny (2KB, growable) stack, so spinning up tens of thousands of them for tens of thousands of concurrent connections is cheap in a way tens of thousands of OS threads never could be. This is precisely why "one goroutine per connection" — the simplest possible design — is also a *reasonable* design in Go, where in C or Java it would require an event loop or thread pool to avoid falling over.

## 6. TLS Termination vs Passthrough

**Termination**: the LB decrypts incoming TLS traffic (it holds the certificate/private key), then talks to backends over plain TCP/HTTP inside the trusted internal network. This is the common case — it centralizes certificate management in one place, and lets the LB actually inspect L7 content (paths, headers) since it can see plaintext. The tradeoff is that traffic between the LB and backends is unencrypted unless you add a second layer of TLS internally (TLS "re-encryption").

**Passthrough**: the LB forwards encrypted TCP bytes without decrypting them at all — this is only possible at L4, since the LB can't read an HTTP path it can't decrypt. It preserves end-to-end encryption but sacrifices any L7-aware routing and pushes certificate management out to every backend.

GoBalance implements termination as the default for L7 (it needs to read requests anyway) and supports either mode conceptually at L4, though v1 focuses on termination since that's what makes the metrics/logging story coherent (you can't log a meaningful request path for traffic you never decrypted).

## 7. Graceful Shutdown and Configuration Reload

A load balancer that drops in-flight connections on every deploy or config change fails the exact job it exists to do — presenting a stable interface over a changing backend fleet.

**Graceful shutdown** means: stop accepting *new* connections immediately, but let already-accepted connections finish naturally, up to some bounded timeout, before actually exiting. Go's `http.Server.Shutdown(ctx)` does this out of the box for the L7 path; the L4 path needs the same idea implemented manually — close the listener, then wait (with a timeout) on a `sync.WaitGroup` tracking in-flight relayed connections.

**Configuration reload** (adding/removing backends, changing an algorithm, rotating a TLS cert) needs to happen without a restart, because a restart *is* a shutdown-then-startup, and during that window you're not load balancing anything. The trick is holding backend-pool state behind something like an `atomic.Pointer` (or a `sync.RWMutex`-protected struct) that in-flight requests read from, and swapping in a new pool atomically once the new config is parsed and validated — readers never see a half-updated, inconsistent pool, and existing connections routed under the old config keep running to completion.

## 8. Resilience Beyond Health Checks

A production-grade LB layers several mechanisms, each catching a different failure mode:

- **Timeouts** at every hop (accepting a connection, dialing a backend, waiting for a response) so a single hung backend can't hold resources indefinitely.
- **Retries** for idempotent requests to a different backend when the first attempt fails outright (not on a partial/ambiguous failure, where retrying could double-apply a non-idempotent operation).
- **Circuit breaking**: after a backend fails repeatedly, stop even *trying* it for a cooldown period instead ogobalancef continuing to dial a server that's clearly down — this reduces load on both the dead backend and the LB itself. (Listed as future work in the PRD, but worth understanding: this is a step beyond simple health-check ejection, since it also stops the connection *attempts*, not just the routing.)
- **Backpressure**: bounding how many concurrent connections/requests the LB will accept in total, so it degrades (rejects new work with a clear error) rather than falling over under overload.

## 9. How This Compares to Real Load Balancers

nginx and HAProxy are event-loop-based and extremely mature, with algorithm and health-check feature sets that are a superset of what's described here — they're the right choice for production traffic. Envoy adds a full xDS control-plane API for dynamic configuration at scale, and is the standard inside modern service-mesh architectures. Cloud load balancers (AWS ALB/NLB, GCP's load balancers) wrap similar concepts in a managed, horizontally-scaled service with DNS-based global routing on top.

GoBalance is a scaled-down version of the same ideas, built to be read end-to-end and fully understood by its author — which is precisely the thing that's hard to claim credibly about a production system you've only configured, not built.
