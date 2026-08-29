package balancer

import (
	"errors"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

var ErrNoHealthyBackends = errors.New("no healthy backends available")

type Balancer interface {
	Pick() (*pool.Backend, error)
}
