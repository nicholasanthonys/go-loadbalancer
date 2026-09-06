package pool

import "testing"

func TestBackend_HysteresisFlipsAtThreshold(t *testing.T) {
	b := &Backend{Addr: "test", healthy: true}
	b.RecordFailure(3)
	b.RecordFailure(3)

	if !b.IsHealthy() {
		t.Fatalf("expected still healty after 2 failures, threshold is 3")

	}

	b.RecordFailure(3)
	if b.IsHealthy() {
		t.Fatalf("expected backend to be unhealthy after 3 failures, threshold is 3")
	}

	b.RecordSuccess(2)
	if b.IsHealthy() {
		t.Fatalf("expected backend to be unhealthy  after 1 successes, threshold is 2")
	}

	b.RecordSuccess(2)
	if !b.IsHealthy() {
		t.Fatalf("expected healthy after 2nd consecutive success")
	}
}

func TestSetBackends_ReusesExistingBackend(t *testing.T) {
	p := New([]string{"10.0.0.1:8080", "10.0.0.2:8080"})

	survivor := p.Backends[0]
	survivor.RecordSuccess(1) // mark it healthy so we can tell if state survives
	survivor.IncActiveConns()

	if !survivor.IsHealthy() {
		t.Fatalf("setup: expected survivor to be healthy before SetBackends")
	}

	p.SetBackends([]BackendSpec{
		{Addr: "10.0.0.1:8080", Weight: 5}, // same address, new weight
		{Addr: "10.0.0.3:8080", Weight: 1}, // brand new address
	})

	all := p.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 backends after SetBackends, got %d", len(all))
	}

	got := all[0]
	if got != survivor {
		t.Fatalf("expected existing backend pointer to be reused for surviving address, got a new *Backend")
	}
	if !got.IsHealthy() {
		t.Fatalf("expected reused backend to keep its healthy state")
	}
	if got.ActiveConns() != 1 {
		t.Fatalf("expected reused backend to keep its active connection count, got %d", got.ActiveConns())
	}
	if got.Weight != 5 {
		t.Fatalf("expected reused backend's weight to be updated to 5, got %d", got.Weight)
	}

	newBackend := all[1]
	if newBackend.Addr != "10.0.0.3:8080" {
		t.Fatalf("expected second backend to be the new address, got %q", newBackend.Addr)
	}
	if newBackend.IsHealthy() {
		t.Fatalf("expected brand-new backend to start unhealthy pending its own health checks")
	}
}
