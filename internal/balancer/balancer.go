package balancer

import (
	"errors"
	"fmt"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

var ErrNoHealthyBackends = errors.New("no healthy backends available")

type Balancer interface {
	Pick() (*pool.Backend, error)
}

func New(name string, p *pool.Pool) (Balancer, error) {
	switch name {
	case "round_robin":
		return NewRoundRobin(p), nil
	case "weighted_round_robin":
		return NewWeightedRoundRobin(p), nil
	case "least_conn":
		return NewLeastConn(p), nil
	case "weighted_least_conn":
		return NewWeightedLeastConn(p), nil
	case "random":
		return NewRandom(p), nil
	case "power_of_two":
		return NewPowerOfTwoChoices(p), nil
	default:
		return nil, fmt.Errorf("unknown algorithm %q", name)
	}

}
