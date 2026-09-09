package pool

import (
	"sync"
	"sync/atomic"
)

type Backend struct {
	Addr             string
	weight           atomic.Int64
	mu               sync.Mutex
	healthy          bool
	consecutiveFails int
	consecutiveOks   int
	activeConns      atomic.Int64
}

// Weight returns the backend's current load-balancing weight. It is read on
// every Pick() by the weighted algorithms and may be updated concurrently by
// a config reload, so it is stored atomically.
func (b *Backend) Weight() int {
	return int(b.weight.Load())
}

// SetWeight updates the backend's load-balancing weight. Called by
// Pool.SetBackends during a config reload while the weighted algorithms may
// be reading it.
func (b *Backend) SetWeight(w int) {
	b.weight.Store(int64(w))
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
	mu       sync.RWMutex
}

func New(addrs []string) *Pool {
	p := &Pool{}
	for _, addr := range addrs {
		b := &Backend{Addr: addr, healthy: false}
		b.SetWeight(1)
		p.Backends = append(p.Backends, b)
	}
	return p
}

func (p *Pool) All() []*Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Backends
}

func (p *Pool) Healthy() []*Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var healthyBackends []*Backend

	for _, backend := range p.Backends {
		if backend.IsHealthy() {
			healthyBackends = append(healthyBackends, backend)
		}
	}
	return healthyBackends
}

type BackendSpec struct {
	Addr   string
	Weight int
}

// SetBackends replaces the pool's backend set from specs, reusing the existing
// *Backend for any address that survives the change so its health state,
// hysteresis counters, and active-connection count carry over. Genuinely new
// addresses start unhealthy, pending their own health checks.
func (p *Pool) SetBackends(specs []BackendSpec) {
	p.mu.Lock()
	defer p.mu.Unlock()

	old := make(map[string]*Backend, len(p.Backends))
	for _, b := range p.Backends {
		old[b.Addr] = b
	}

	newBackends := make([]*Backend, 0, len(specs))
	for _, spec := range specs {
		if b, ok := old[spec.Addr]; ok {
			b.SetWeight(spec.Weight)
			newBackends = append(newBackends, b)
		} else {
			nb := &Backend{Addr: spec.Addr, healthy: false}
			nb.SetWeight(spec.Weight)
			newBackends = append(newBackends, nb)
		}
	}
	p.Backends = newBackends
}
