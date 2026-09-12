package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

func TestBackendCollector_ReportsLiveState(t *testing.T) {
	p := pool.New([]string{"10.0.0.1:8080", "10.0.0.2:8080"})

	healthy := p.Backends[0]
	healthy.RecordSuccess(1)
	healthy.IncActiveConns()
	healthy.IncActiveConns()

	// p.Backends[1] is left at its zero-value state: unhealthy, no active
	// conns — exactly what a brand-new backend looks like before its first
	// successful health check.

	c := &BackendCollector{Pools: map[string]*pool.Pool{"web": p}}

	want := `
# HELP gobalance_backend_active_connections Current active connections per backend.
# TYPE gobalance_backend_active_connections gauge
gobalance_backend_active_connections{backend="10.0.0.1:8080",listener="web"} 2
gobalance_backend_active_connections{backend="10.0.0.2:8080",listener="web"} 0
# HELP gobalance_backend_healthy 1 if the backend is currently healthy, else 0.
# TYPE gobalance_backend_healthy gauge
gobalance_backend_healthy{backend="10.0.0.1:8080",listener="web"} 1
gobalance_backend_healthy{backend="10.0.0.2:8080",listener="web"} 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"gobalance_backend_active_connections", "gobalance_backend_healthy"); err != nil {
		t.Fatal(err)
	}
}

// TestBackendCollector_ReflectsStateAtScrapeTime confirms the collector
// reads Backend state live rather than caching it: two Collect calls
// against the same *Backend, with a state change in between, must produce
// two different readings.
func TestBackendCollector_ReflectsStateAtScrapeTime(t *testing.T) {
	p := pool.New([]string{"10.0.0.1:8080"})
	c := &BackendCollector{Pools: map[string]*pool.Pool{"web": p}}

	if got := testutil.CollectAndCount(c); got != 2 {
		t.Fatalf("expected 2 metrics (active conns + healthy) for 1 backend, got %d", got)
	}

	before := `
# HELP gobalance_backend_healthy 1 if the backend is currently healthy, else 0.
# TYPE gobalance_backend_healthy gauge
gobalance_backend_healthy{backend="10.0.0.1:8080",listener="web"} 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(before), "gobalance_backend_healthy"); err != nil {
		t.Fatalf("before RecordSuccess: %v", err)
	}

	p.Backends[0].RecordSuccess(1)

	after := `
# HELP gobalance_backend_healthy 1 if the backend is currently healthy, else 0.
# TYPE gobalance_backend_healthy gauge
gobalance_backend_healthy{backend="10.0.0.1:8080",listener="web"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(after), "gobalance_backend_healthy"); err != nil {
		t.Fatalf("after RecordSuccess: %v", err)
	}
}
