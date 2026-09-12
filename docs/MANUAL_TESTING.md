# Manual Testing: Phase 7 (Logging + Metrics)

Step-by-step check that structured logging, Prometheus metrics, and the
health-transition wiring actually work end to end — not just that the code
compiles. Run each step in a separate terminal as noted.

## 0. Clear any leftover ports (if you tested before)

```bash
for p in 9090 9091 9100 9101 9102 9103; do lsof -iTCP:$p -sTCP:LISTEN -n -P; done
```

Kill anything listed that's a stale `python3 -m http.server` or `gobalance` from a
previous run: `kill <PID>`.

## 1. Generate a dev TLS cert (skip if `certs/` already exists)

```bash
mkdir -p certs
openssl req -x509 -newkey rsa:2048 -nodes -keyout certs/key.pem -out certs/cert.pem -days 365 -subj "/CN=localhost"
```

## 2. Start three dummy backends — Terminal A

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

## 3. Start gobalance — Terminal B

```bash
go run ./cmd/gobalance
```

Wait ~10s for health checks to mark all three backends healthy (default: 2
consecutive successful checks). Leave this terminal visible — this is where
the JSON log lines from `internal/logging` will appear.

## 4. Generate traffic — Terminal C

```bash
for i in $(seq 1 20); do curl -sk https://localhost:9091/index.html; done
```

Responses should cycle across `backend 9101/9102/9103`.

## 5. Confirm metrics are moving — Terminal C

```bash
curl -s http://localhost:9100/metrics | grep gobalance_
```

Expect to see, with nonzero/plausible values:

- `gobalance_requests_total{listener="...",status="200"}`
- `gobalance_request_duration_seconds_bucket{...}` / `_sum` / `_count`
- `gobalance_backend_healthy{listener="...",backend="127.0.0.1:910x"} 1` for all three
- `gobalance_backend_active_connections{...}` (near 0 between requests, for L7)

## 6. Kill a backend and watch it flip — the real test of this phase

```bash
kill %1   # kills the 9101 python server started as job 1 in step 2
```

Within one health-check interval, expect in **Terminal B**:

```json
{"level":"INFO","msg":"backend health changed","backend":"127.0.0.1:9101","healthy":false}
```

Then in **Terminal C**:

```bash
curl -s http://localhost:9100/metrics | grep 'gobalance_backend_healthy{listener="web",backend="127.0.0.1:9101"}'
```

should now read `0`. Repeat step 4's curl loop — traffic should only ever
land on 9102/9103.

## 7. Reload test

Edit `configs/example.yaml` (e.g. bump a backend's weight) and save it, **or**:

```bash
kill -HUP $(pgrep -f gobalance)
```

Expect in Terminal B a `"reload applied"` log line, and in `/metrics`:

```bash
curl -s http://localhost:9100/metrics | grep gobalance_reloads_total
```

`gobalance_reloads_total{result="success"}` should have incremented by 1.

## 8. Clean up

```bash
kill %1 %2 %3 2>/dev/null   # remaining python backends
# Ctrl+C in Terminal B to stop gobalance
```

## What "done" looks like

- [ ] Traffic cycles across all three backends (step 4)
- [ ] `/metrics` shows real counters/histograms/gauges (step 5)
- [ ] Killing a backend produces a JSON health-transition log line (step 6)
- [ ] `gobalance_backend_healthy` flips to `0` for the killed backend (step 6)
- [ ] Traffic stops routing to the killed backend (step 6)
- [ ] A reload (file save or `SIGHUP`) increments `gobalance_reloads_total` and logs `"reload applied"` (step 7)
