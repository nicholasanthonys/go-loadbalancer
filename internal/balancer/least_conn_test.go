package balancer

import (
	"sync"
	"testing"
	"time"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

// simulateLoad fires totalRequests concurrent "requests" through pick.
// Each request holds the picked backend's connection open for a duration
// that depends on the backend's address (slowAddr gets a long hold, to
// stand in for an overloaded/slow real backend), then releases it. This
// exercises ActiveConns tracking exactly like the real proxy layer does,
// without needing real network I/O.
func simulateLoad(t *testing.T, pick func() (*pool.Backend, error), slowAddr string, totalRequests int) map[string]int {
	t.Helper()

	counts := make(map[string]int)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			backend, err := pick()
			if err != nil {
				t.Errorf("unexpected error: %s", err.Error())
				return
			}

			backend.IncActiveConns()
			mu.Lock()
			counts[backend.Addr]++
			mu.Unlock()

			if backend.Addr == slowAddr {
				time.Sleep(200 * time.Millisecond)
			} else {
				time.Sleep(5 * time.Millisecond)
			}
			backend.DecActiveConns()
		}()
		// Stagger arrivals — without this, all requests fire and pick
		// before the slow backend's 200ms hold has any chance to
		// influence later picks, collapsing to an even split regardless
		// of algorithm. Real traffic arrives over time, not all at once.
		time.Sleep(time.Millisecond)
	}
	wg.Wait()
	return counts
}

func newHealthyPool(addrs []string) *pool.Pool {
	p := pool.New(addrs)
	for _, b := range p.Backends {
		b.RecordSuccess(1)
	}
	return p
}

func TestLeastConn_FavorsFasterBackendsOverRoundRobin(t *testing.T) {
	const slowAddr = "slow"
	const totalRequests = 300

	rrPool := newHealthyPool([]string{"fast-A", "fast-B", slowAddr})
	rr := NewRoundRobin(rrPool)
	rrCounts := simulateLoad(t, rr.Pick, slowAddr, totalRequests)

	lcPool := newHealthyPool([]string{"fast-A", "fast-B", slowAddr})
	lc := &LeastConn{pool: lcPool}
	lcCounts := simulateLoad(t, lc.Pick, slowAddr, totalRequests)

	t.Logf("RoundRobin distribution: %v", rrCounts)
	t.Logf("LeastConn distribution:  %v", lcCounts)

	if lcCounts[slowAddr] >= rrCounts[slowAddr] {
		t.Errorf("expected LeastConn to route fewer requests to the slow backend than RoundRobin; RoundRobin sent it %d, LeastConn sent it %d", rrCounts[slowAddr], lcCounts[slowAddr])
	}
}
