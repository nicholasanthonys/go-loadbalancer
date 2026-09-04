package balancer

import "github.com/nicholasanthonys/gobalance/internal/pool"

type LeastConn struct {
	pool *pool.Pool
}

func NewLeastConn(p *pool.Pool) *LeastConn {
	return &LeastConn{pool: p}
}

func (l *LeastConn) Pick() (*pool.Backend, error) {
	backends := l.pool.Healthy()
	if len(backends) == 0 {
		return nil, ErrNoHealthyBackends
	}

	best := backends[0]
	for _, b := range backends[1:] {
		if b.ActiveConns() < best.ActiveConns() {
			best = b
		}
	}
	return best, nil
}
