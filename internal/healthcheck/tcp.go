package healthcheck

import (
	"context"
	"net"
	"time"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

type Checker struct {
	Pool           *pool.Pool
	Interval       time.Duration
	Timeout        time.Duration
	UnhealthyTresh int
	HealthyThresh  int
}

func (c *Checker) Run(ctx context.Context) {
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, b := range c.Pool.All() {
				go c.checkOne(b) // fan out; don't let one slow backend delay the rest
			}
		}
	}
}

func (c *Checker) checkOne(b *pool.Backend) {
	conn, err := net.DialTimeout("tcp", b.Addr, c.Timeout)
	if err != nil {
		// Handle error
		b.RecordFailure(c.UnhealthyTresh)
		return
	}
	defer conn.Close()
	b.RecordSuccess(c.HealthyThresh)
}
