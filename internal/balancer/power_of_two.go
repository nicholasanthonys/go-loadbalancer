package balancer

import (
	"math/rand"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

type PowerOfTwoChoices struct {
	pool *pool.Pool
}

func NewPowerOfTwoChoices(p *pool.Pool) *PowerOfTwoChoices {
	return &PowerOfTwoChoices{pool: p}
}

func (p2c *PowerOfTwoChoices) Pick() (*pool.Backend, error) {
	backends := p2c.pool.Healthy()
	if len(backends) == 0 {
		return nil, ErrNoHealthyBackends
	}
	if len(backends) == 1 {
		return backends[0], nil
	}

	// rand.Intn(len(backends)) for the first index; for the
	// second, rand.Intn(len(backends)-1), then nudge it past the first
	// index if it would otherwise land on or after it. This guarantees
	// two distinct indices with no retry loop needed.

	firstIdx := rand.Intn(len(backends))
	secondIdx := rand.Intn(len(backends) - 1)
	if secondIdx >= firstIdx {
		secondIdx++
	}

	a := backends[firstIdx]
	b := backends[secondIdx]
	if a.ActiveConns() <= b.ActiveConns() {
		return a, nil
	}
	return b, nil
}
