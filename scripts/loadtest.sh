#!/usr/bin/env bash
# Phase 9 load test: spins up 3 dummy backends + a -race gobalance instance,
# then runs vegeta against the L7 listener for a baseline, a reload-under-load
# scenario, and a backend-kill scenario. Reports land in loadtest-results/.
#
# Usage: scripts/loadtest.sh [rate] [duration]
#   scripts/loadtest.sh            # 2000 req/s for 30s (default)
#   scripts/loadtest.sh 5000 30s   # PRD target rate
set -euo pipefail
cd "$(dirname "$0")/.."

RATE="${1:-2000}"
DURATION="${2:-30s}"
BACKEND_PORTS=(9101 9102 9103)
L7_ADDR="http://localhost:9091"
CONFIG="configs/example.yaml"
BIN="./gobalance-race"
RESULTS_DIR="loadtest-results"

if ! command -v vegeta >/dev/null; then
  echo "vegeta not found on PATH (try: go install github.com/tsenart/vegeta@latest)" >&2
  exit 1
fi

mkdir -p "$RESULTS_DIR"
PIDS=()

cleanup() {
  echo
  echo "== cleaning up =="
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

clear_port() {
  local pid
  pid=$(lsof -tiTCP:"$1" -sTCP:LISTEN -n -P 2>/dev/null || true)
  if [[ -n "$pid" ]]; then
    echo "port $1 in use by pid $pid, killing"
    kill "$pid" 2>/dev/null || true
    sleep 0.5
  fi
}

echo "== clearing stale ports =="
for p in "${BACKEND_PORTS[@]}" 9090 9091 9100; do
  clear_port "$p"
done

echo "== starting dummy backends =="
# Plain `python3 -m http.server` speaks HTTP/1.0 and closes the TCP connection
# after every response. At load-test rates that forces a brand new connection
# (and a brand new ephemeral port) per request, which exhausts the local port
# range and produces misleading multi-second latencies that have nothing to do
# with gobalance. This tiny HTTP/1.1 variant keeps connections alive instead,
# like a real backend would.
cat >/tmp/keepalive_backend.py <<'PYEOF'
import http.server
import socket
import sys

class KeepAliveHandler(http.server.SimpleHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def setup(self):
        super().setup()
        # Without this, the handler's separate header/body writes trigger
        # Nagle's algorithm + Linux's ~40ms delayed-ACK timer, adding a fixed
        # ~40ms to every request that has nothing to do with gobalance itself.
        self.connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)

if __name__ == "__main__":
    port = int(sys.argv[1])
    directory = sys.argv[2]
    handler = lambda *args, **kwargs: KeepAliveHandler(*args, directory=directory, **kwargs)
    http.server.ThreadingHTTPServer(("127.0.0.1", port), handler).serve_forever()
PYEOF

for i in 1 2 3; do
  port="${BACKEND_PORTS[$((i - 1))]}"
  dir="/tmp/backend$i"
  mkdir -p "$dir"
  echo "Hello from backend $port" >"$dir/index.html"
  echo ok >"$dir/healthz"
  python3 /tmp/keepalive_backend.py "$port" "$dir" >/tmp/backend$i.log 2>&1 &
  PIDS+=($!)
done
BACKEND_PID_0="${PIDS[0]}" # port 9101 — used by the failure-injection step below

echo "== building + starting gobalance (race build, -tls=false) =="
if [[ ! -x "$BIN" ]]; then
  go build -race -o "$BIN" ./cmd/gobalance
fi
"$BIN" -config="$CONFIG" -tls=false >"$RESULTS_DIR/gobalance.log" 2>&1 &
GOBALANCE_PID=$!
PIDS+=("$GOBALANCE_PID")

echo "waiting for backends to pass health checks..."
sleep 10

echo
echo "== 1/3 baseline: $RATE req/s for $DURATION against $L7_ADDR =="
echo "GET $L7_ADDR/" | vegeta attack -duration="$DURATION" -rate="$RATE" -output="$RESULTS_DIR/baseline.bin"
vegeta report "$RESULTS_DIR/baseline.bin" | tee "$RESULTS_DIR/baseline.txt"

echo
echo "== 2/3 reload-under-load: SIGHUP fired 5s into a 20s attack =="
echo "GET $L7_ADDR/" | vegeta attack -duration=20s -rate=1000 -output="$RESULTS_DIR/reload.bin" &
attack_pid=$!
sleep 5
kill -HUP "$GOBALANCE_PID"
wait "$attack_pid"
vegeta report "$RESULTS_DIR/reload.bin" | tee "$RESULTS_DIR/reload.txt"

echo
echo "== 3/3 failure injection: backend :9101 killed 5s into a 20s attack =="
echo "GET $L7_ADDR/" | vegeta attack -duration=20s -rate=1000 -output="$RESULTS_DIR/failure.bin" &
attack_pid=$!
sleep 5
kill "$BACKEND_PID_0" 2>/dev/null || true
wait "$attack_pid"
vegeta report "$RESULTS_DIR/failure.bin" | tee "$RESULTS_DIR/failure.txt"

echo
echo "reports + gobalance log written to $RESULTS_DIR/"
