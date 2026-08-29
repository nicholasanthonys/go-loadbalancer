package balancer

import (
	"errors"
	"testing"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

func TestRoundRobin_CyclesInOrder(t *testing.T) {
	p := pool.New([]string{"A", "B", "C"})
	rr := NewRoundRobin(p)
	p.Backends[0].RecordSuccess(1)
	p.Backends[1].RecordSuccess(1)
	p.Backends[2].RecordSuccess(1)

	expectedOrder := []string{"A", "B", "C", "A", "B", "C"}
	for i := 0; i < len(expectedOrder); i++ {
		actual, err := rr.Pick()
		if err != nil {
			t.Fatalf("unexpected error %s", err.Error())
		}
		if actual == nil {
			t.Fatalf("expected a backend, got nil")
		}
		if actual.Addr != expectedOrder[i] {
			t.Errorf("Expected %s, got %s", expectedOrder[i], actual.Addr)
		}
	}
}

func TestRoundRobin_NoHealthyBackends(t *testing.T) {
	p := pool.New([]string{})
	rr := NewRoundRobin(p)

	_, err := rr.Pick()
	if err == nil {
		t.Fatalf("Expected error, got nil")
	}
	if !errors.Is(err, ErrNoHealthyBackends) {
		t.Errorf("Expected ErrNoHealthyBackends, got %s", err.Error())
	}
}
