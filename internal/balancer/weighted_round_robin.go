package balancer

import (
	"sync"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

type WeightedRoundRobin struct {
	pool    *pool.Pool
	mu      sync.Mutex
	current map[*pool.Backend]int
}

func NewWeightedRoundRobin(p *pool.Pool) *WeightedRoundRobin {
	return &WeightedRoundRobin{
		pool:    p,
		current: make(map[*pool.Backend]int),
	}
}

func (w *WeightedRoundRobin) Pick() (*pool.Backend, error) {
	backends := w.pool.Healthy()
	if len(backends) == 0 {
		return nil, ErrNoHealthyBackends
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	totalWeight := 0
	var best *pool.Backend

	for _, b := range backends {
		w.current[b] += b.Weight
		totalWeight += b.Weight
		if best == nil || w.current[b] > w.current[best] {
			best = b
		}
	}

	// subtract totalWeight from w.current[best]
	w.current[best] -= totalWeight

	return best, nil
}
