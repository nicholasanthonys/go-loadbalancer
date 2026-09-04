package balancer

import (
	"errors"
	"fmt"
	"testing"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

func TestRandom_RandomPick(t *testing.T) {
	p := newHealthyPool([]string{"A", "B", "C"})
	r := NewRandom(p)

	const picks = 900
	counts := map[string]int{}
	for i := 0; i < picks; i++ {
		backend, err := r.Pick()
		if err != nil {
			t.Fatalf("unexpected error %s", err.Error())
		}
		counts[backend.Addr]++
	}

	wantA := picks / 3
	wantB := picks / 3
	wantC := picks / 3
	tolerance := picks / 10 // 10% tolerance

	fmt.Println("A", counts["A"])
	fmt.Println("B", counts["B"])
	fmt.Println("C", counts["C"])
	if diff := abs(counts["A"] - wantA); diff > tolerance {
		t.Errorf("A: got %d picks, want ~%d (tolerance %d)", counts["A"], wantA, tolerance)
	}
	if diff := abs(counts["B"] - wantB); diff > tolerance {
		t.Errorf("B: got %d picks, want ~%d (tolerance %d)", counts["B"], wantB, tolerance)
	}
	if diff := abs(counts["C"] - wantC); diff > tolerance {
		t.Errorf("C: got %d picks, want ~%d (tolerance %d)", counts["C"], wantC, tolerance)
	}
}

func TestRandom_NoHealthyBackends(t *testing.T) {
	p := pool.New([]string{"A", "B", "C"}) // Never marked healthy

	if _, err := NewRandom(p).Pick(); !errors.Is(err, ErrNoHealthyBackends) {
		t.Fatalf("expected ErrNoHealthyBackends, got %v", err)
	}
}
