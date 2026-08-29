package pool

import "sync"

type Backend struct {
	Addr             string
	Weight           int
	mu               sync.Mutex
	healthy          bool
	consecutiveFails int
	consecutiveOks   int
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
