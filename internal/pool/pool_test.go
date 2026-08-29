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
