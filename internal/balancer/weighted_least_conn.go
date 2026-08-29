package balancer

import "github.com/nicholasanthonys/gobalance/internal/pool"

type WeightedLeastConn struct {
	pool *pool.Pool
}

func NewWeightedLeastConn(p *pool.Pool) *WeightedLeastConn {
	return &WeightedLeastConn{
		pool: p,
	}
}

func (w *WeightedLeastConn) Pick() (*pool.Backend, error) {

	backends := w.pool.Healthy()
	if len(backends) == 0 {
		return nil, ErrNoHealthyBackends
	}

	best := backends[0]
	for _, b := range backends[1:] {
		// b active con / b.weight = best active con / best.weight
		// Cross multiply to avoid division and floating point comparison
		if int(b.ActiveConns())*best.Weight < int(best.ActiveConns())*b.Weight {
			best = b
		}
	}
	return best, nil
}
