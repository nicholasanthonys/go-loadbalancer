# Product Requirements Document: GoBalance
### A Layer 4 / Layer 7 Load Balancer in Go

**Author:** Nicholas
**Status:** Draft v1.0
**Last updated:** 2026-08-13

---

## 1. Overview

GoBalance is a self-contained software load balancer written in Go that distributes incoming traffic across a pool of backend servers. It operates at both Layer 4 (raw TCP) and Layer 7 (HTTP), supports multiple routing algorithms, performs active and passive health checks, terminates TLS, exposes operational metrics, and can reload its configuration without dropping connections.

This is a portfolio project. The primary goal is to demonstrate systems-level engineering ability — concurrency, networking, fault tolerance, and observability — in a project that is scoped small enough to finish, but deep enough to talk about confidently in an interview.

## 2. Problem Statement

Anyone can put "load balancer" on a resume after skimming an nginx config. Building one from scratch forces engagement with the actual hard problems: how do you route thousands of concurrent connections without blocking? How do you know a backend is dead before a user finds out? How do you swap configuration in a live process without severing existing connections? How do you avoid sending traffic to a server that's still starting up, or a server that's silently failing behind a 200 status?

There is no shortage of production load balancers (nginx, HAProxy, Envoy, cloud-managed LBs). GoBalance isn't trying to replace them. It exists to prove, in code, that the person who built it understands what those tools are doing under the hood.

## 3. Goals

- Build a working software load balancer that operates at both TCP (L4) and HTTP (L7).
- Implement and correctly compare multiple load-balancing algorithms.
- Implement real health checking with configurable thresholds, not a superficial ping.
- Support TLS termination.
- Support zero-downtime configuration reload.
- Expose Prometheus-compatible metrics and structured logs suitable for a real observability stack.
- Produce a project that is easy to demo in under five minutes (Docker Compose, a few backend containers, a script that kills one and shows failover live).
- Produce documentation (this PRD, a tech stack doc, a theory doc, and a tutorial) that shows the reasoning behind the design, not just the code.

## 4. Non-Goals

- Global/geo load balancing (DNS-based, anycast, multi-region routing) — out of scope.
- Full API-gateway feature set: no auth, no request transformation, no WAF, no complex L7 routing DSL.
- Kubernetes ingress controller packaging (a plain Docker deployment is sufficient).
- Beating HAProxy/Envoy on raw throughput. Correctness and clarity matter more than squeezing out the last 5% of performance.
- A GUI. A minimal admin/metrics HTTP endpoint is enough.

## 5. Target "Users"

Since this is a portfolio piece, there are really two audiences:

1. **You, running it locally or in Docker Compose** to demo behavior — failover, algorithm choice, hot reload — to yourself or an interviewer.
2. **A technical reviewer** (interviewer, hiring manager, or another engineer) reading the code and docs to assess design judgment.

Both audiences care about the same thing: does the system behave correctly under failure, and can the author explain why it's built the way it is.

## 6. Functional Requirements

### 6.1 Core proxying (L4)

- Accept raw TCP connections on a configurable listen address/port.
- Forward each accepted connection to a backend selected by the active algorithm, and pipe bytes bidirectionally until either side closes.
- Support multiple independent backend pools ("listeners"), each with its own algorithm and backend list, defined in configuration.

### 6.2 HTTP reverse proxying (L7)

- Accept HTTP/1.1 (and HTTP/2 where practical) requests and forward them to a backend using Go's `net/http/httputil.ReverseProxy` as the transport layer.
- Preserve/adjust standard proxy headers (`X-Forwarded-For`, `X-Forwarded-Proto`, `X-Forwarded-Host`, `Via`).
- Support path-prefix based routing to different backend pools (e.g. `/api/*` → pool A, `/*` → pool B) as a stretch requirement.

### 6.3 Load-balancing algorithms

The following must be implemented, unit-tested, and selectable via config per listener:

| Algorithm | Behavior |
|---|---|
| Round robin | Cycles through healthy backends in order. |
| Weighted round robin | Same, but backends with higher weight receive proportionally more requests. |
| Least connections | Routes to the healthy backend with the fewest active connections. |
| Weighted least connections | Least connections, normalized by backend weight/capacity. |
| Random / random-two-choices | Baseline for comparison; power-of-two-choices as a stretch goal. |

### 6.4 Health checking

- **Active checks:** periodically probe each backend (TCP connect for L4 pools, HTTP GET to a configurable path with expected status code for L7 pools) on a configurable interval.
- **Thresholds:** a backend is marked unhealthy after N consecutive failed checks, and marked healthy again only after M consecutive successful checks (hysteresis, to prevent flapping).
- **Passive checks:** L7 proxy tracks live request outcomes (connection errors, timeouts) and can eject a backend early without waiting for the next active check cycle.
- Unhealthy backends are removed from rotation immediately; they continue to be probed so they can rejoin automatically.

### 6.5 TLS

- Terminate TLS on the listener (load a cert/key pair from config).
- Forward to backends over plain HTTP/TCP within the trusted network (TLS termination, not passthrough, is the default mode).
- Support hot-reloading the certificate without restarting the process (nice-to-have).

### 6.6 Configuration

- Declarative config file (YAML) describing listeners, backend pools, algorithm choice, health-check parameters, and TLS settings.
- Config validated at load time with clear error messages for malformed input.
- Config can be reloaded at runtime (via `SIGHUP` or a file-watch) without dropping in-flight connections.

### 6.7 Observability

- Structured logs (JSON) for connection lifecycle events, health-check state transitions, and config reloads.
- `/metrics` endpoint in Prometheus exposition format: request counts, error counts, latency histograms, active connection gauges, backend health state, per-backend request distribution.
- A minimal `/healthz` endpoint for the load balancer's own liveness.

### 6.8 Lifecycle

- Graceful shutdown: stop accepting new connections, drain in-flight ones within a configurable timeout, then exit.
- Clean startup/shutdown logging so behavior is visible in a demo.

## 7. Non-Functional Requirements

- **Concurrency correctness:** no data races under `go test -race`; connection-count and health-state updates must be safe under concurrent access.
- **Performance:** sustain at least 5,000 req/s on a single core against local backends with p99 added latency under 5ms attributable to the proxy layer, verified with a load-testing tool (documented in the tech stack doc). This is a target for learning purposes, not a competitive benchmark.
- **Resilience:** a single backend crashing must not cause client-visible errors beyond the in-flight requests actively touching it at the moment of failure; the LB must detect and route around it within one health-check interval.
- **Portability:** runs as a single static binary; ships with a Dockerfile and a docker-compose demo environment.
- **Testability:** core algorithm and health-check logic covered by unit tests; end-to-end proxy behavior covered by integration tests using `httptest` and real backend goroutines.

## 8. Success Metrics

Since there are no real end users, success is measured by demonstrable, explainable behavior:

- All five algorithms produce visibly correct distribution patterns under a scripted test (e.g. round robin cycles in order; least-connections favors an artificially slow backend less).
- Killing a backend container mid-load-test shows automatic failover in logs/metrics within one health-check interval, with zero or near-zero client-visible errors.
- Config reload (adding/removing a backend, changing algorithm) takes effect with no dropped connections, demonstrated live.
- `go test -race ./...` passes clean.
- The project has a README a stranger could use to run the demo in under 5 minutes.

## 9. Milestones

1. **M1 — L4 core:** TCP proxy, round robin, static backend list, basic logging.
2. **M2 — Health checks:** active TCP health checks with hysteresis; unhealthy backends excluded from rotation.
3. **M3 — L7 layer:** HTTP reverse proxy on top of the same backend-pool abstraction; HTTP health checks.
4. **M4 — Algorithms:** weighted round robin, least connections, weighted least connections.
5. **M5 — TLS + config:** TLS termination; YAML config; hot reload.
6. **M6 — Observability:** structured logging, Prometheus metrics endpoint.
7. **M7 — Hardening:** graceful shutdown, race-condition audit, load testing, failure-injection demo.
8. **M8 — Packaging:** Dockerfile, docker-compose demo, README, architecture diagram, short demo recording.

This maps directly onto the phases in the accompanying tutorial document.

## 10. Risks & Open Questions

- **Scope creep:** service discovery, sticky sessions, and rate limiting are tempting additions; they're deliberately deferred to a "v2" list so the core project stays finishable.
- **HTTP/2 and connection reuse:** `httputil.ReverseProxy` handles most of this, but backend connection pooling behavior under least-connections routing needs care — a pooled idle connection shouldn't be miscounted as an "active" one.
- **Health-check cost at scale:** active checks add load to backends; interval/timeout defaults need to be sane out of the box (documented in the theory doc).
- **What counts as "done" for a portfolio project:** the milestone list above is the definition of done. Anything beyond M8 (service discovery, admin UI, multi-region) is explicitly future work, listed so a reviewer can see the scope was a deliberate choice, not an oversight.

## 11. Future Work (Explicitly Out of Scope for v1)

- Service discovery integration (Consul, etcd, Kubernetes endpoints API).
- Sticky sessions / session affinity.
- Rate limiting and circuit breaking.
- Admin dashboard UI.
- Multi-region / DNS-based global load balancing.
