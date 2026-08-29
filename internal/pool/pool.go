package pool

import (
	"sync"
	"sync/atomic"
)

type Backend struct {
	Addr             string
	Weight           int
	mu               sync.Mutex
	healthy          bool
	consecutiveFails int
	consecutiveOks   int
	activeConns      atomic.Int64
}

// IncActiveConns records that a connection/request has just been routed
// to this backend. Pair with a deferred DecActiveConns wherever a
// connection's lifecycle is known to start and end.
func (b *Backend) IncActiveConns() {
	b.activeConns.Add(1)
}

func (b *Backend) DecActiveConns() {
	b.activeConns.Add(-1)
}

func (b *Backend) ActiveConns() int64 {
	return b.activeConns.Load()
}

func (b *Backend) RecordFailure(threshold int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutiveFails += 1
	b.consecutiveOks = 0

	if b.consecutiveFails >= threshold {
		b.healthy = false
	}
}

func (b *Backend) RecordSuccess(threshold int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutiveOks += 1
	b.consecutiveFails = 0

	if b.consecutiveOks >= threshold {
		b.healthy = true
	}
}

func (b *Backend) IsHealthy() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.healthy
}

type Pool struct {
	Backends []*Backend
}

func New(addrs []string) *Pool {

	p := &Pool{}
	for _, addr := range addrs {
		p.Backends = append(p.Backends, &Backend{Addr: addr, Weight: 1, healthy: false, consecutiveFails: 0, consecutiveOks: 0})
	}
	return p
}

func (p *Pool) All() []*Backend {
	return p.Backends
}

func (p *Pool) Healthy() []*Backend {
	var healthyBackends []*Backend

	for _, backend := range p.Backends {
		if backend.IsHealthy() {
			healthyBackends = append(healthyBackends, backend)
		}
	}
	return healthyBackends
}
