package balancer

import (
	"sync/atomic"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

type RoundRobin struct {
	pool *pool.Pool
	next uint64
}

func NewRoundRobin(pool *pool.Pool) *RoundRobin {
	return &RoundRobin{
		pool: pool,
		next: 0,
	}
}

func (r *RoundRobin) Pick() (*pool.Backend, error) {
	backends := r.pool.Healthy()
	if len(backends) == 0 {
		return nil, ErrNoHealthyBackends
	}

	// prevent data race by using atomic operations to increment the next index
	n := atomic.AddUint64(&r.next, 1)

	// call 1: n=1 → (1-1)%3 = 0 → A
	// call 2: n=2 → (2-1)%3 = 1 → B
	// call 3: n=3 → (3-1)%3 = 2 → C
	// call 4: n=4 → (4-1)%3 = 0 → A again

	return backends[(n-1)%uint64(len(backends))], nil

}
