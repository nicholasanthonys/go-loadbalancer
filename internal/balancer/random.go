package balancer

import (
	"math/rand"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

type Random struct {
	pool *pool.Pool
}

func NewRandom(p *pool.Pool) *Random {
	return &Random{pool: p}
}

func (r *Random) Pick() (*pool.Backend, error) {
	backends := r.pool.Healthy()
	if len(backends) == 0 {
		return nil, ErrNoHealthyBackends
	}
	return backends[rand.Intn(len(backends))], nil
}
