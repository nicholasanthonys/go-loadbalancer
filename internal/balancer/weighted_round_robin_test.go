package balancer

import (
	"testing"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

func TestWeightedRoundRobin_MatchesWeightRatio(t *testing.T) {
	p := pool.New([]string{"A", "B"})
	p.Backends[0].Weight = 3
	p.Backends[1].Weight = 1
	p.Backends[0].RecordSuccess(1)
	p.Backends[1].RecordSuccess(1)

	wrr := NewWeightedRoundRobin(p)

	const picks = 10000
	counts := map[string]int{}
	for i := 0; i < picks; i++ {
		backend, err := wrr.Pick()
		if err != nil {
			t.Fatalf("unexpected error %s", err.Error())
		}
		counts[backend.Addr]++
	}

	// Weight ratio is 3:1, so out of 4 total weight, A should get 3/4
	// and B should get 1/4 of the picks.
	wantA := picks * 3 / 4
	wantB := picks * 1 / 4
	tolerance := picks / 20 // 5%

	if diff := abs(counts["A"] - wantA); diff > tolerance {
		t.Errorf("A: got %d picks, want ~%d (tolerance %d)", counts["A"], wantA, tolerance)
	}
	if diff := abs(counts["B"] - wantB); diff > tolerance {
		t.Errorf("B: got %d picks, want ~%d (tolerance %d)", counts["B"], wantB, tolerance)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
