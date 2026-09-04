package balancer

import (
	"errors"
	"testing"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

// The core P2C property: a heavily-loaded backend loses every comparison
// it's part of, so it should essentially never be returned — while the
// idle backends share the traffic.
func TestPowerOfTwo_AvoidsMostLoadedBackend(t *testing.T) {
	p := newHealthyPool([]string{"A", "B", "C"})
	p2c := NewPowerOfTwoChoices(p)

	for i := 0; i < 100; i++ {
		p.Backends[2].IncActiveConns() // C has more active connections(most loaded backend); but A and B are idle
	}

	counts := map[string]int{}
	for i := 0; i < 900; i++ {
		b, err := p2c.Pick()
		if err != nil {
			t.Fatalf("unexpected error %s", err.Error())
		}
		counts[b.Addr]++
	}

	// Whenever C is one of the two sampled, the other is A or B with 0
	// conns, so C always loses. This is deterministic, not statistical.
	if counts["C"] != 0 {
		t.Errorf("C had 100 active conns; expected 0 picks, got %d", counts["C"])
	}
	if counts["A"] == 0 || counts["B"] == 0 {
		t.Errorf("expected A and B to share traffic, got A=%d B=%d", counts["A"], counts["B"])
	}
}

// The len==1 fast path in Pick().
func TestPowerOfTwoChoices_SingleHealthyBackend(t *testing.T) {

	p := newHealthyPool([]string{"A"})
	p2c := NewPowerOfTwoChoices(p)

	b, err := p2c.Pick()
	if err != nil {
		t.Fatalf("unexpected error %s", err.Error())
	}
	if b.Addr != "A" {
		t.Errorf("expected A, got %s", b.Addr)
	}
}

func TestPowerOfTwoChoices_NoHealthyBackends(t *testing.T) {
	p := pool.New([]string{"A"}) // never marked healthy
	p2c := NewPowerOfTwoChoices(p)

	if _, err := p2c.Pick(); !errors.Is(err, ErrNoHealthyBackends) {
		t.Fatalf("got %v, want ErrNoHealthyBackends", err)
	}

}

func TestPowerOfTwoChoices_FavorsFasterBackendsOverRoundRobin(t *testing.T) {
	const slowAddr = "slow"
	const totalRequests = 300

	rrPool := newHealthyPool([]string{"fast-A", "fast-B", slowAddr})
	rrCounts := simulateLoad(t, NewRoundRobin(rrPool).Pick, slowAddr, totalRequests)

	p2cPool := newHealthyPool([]string{"fast-A", "fast-B", slowAddr})
	p2cCounts := simulateLoad(t, NewPowerOfTwoChoices(p2cPool).Pick, slowAddr, totalRequests)

	t.Logf("RoundRobin: %v", rrCounts)
	t.Logf("P2C:        %v", p2cCounts)

	if p2cCounts[slowAddr] >= rrCounts[slowAddr] {
		t.Errorf("expected P2C to route fewer requests to the slow backend than RoundRobin; RR sent %d, P2C sent %d",
			rrCounts[slowAddr], p2cCounts[slowAddr])
	}

}
