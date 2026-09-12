package healthcheck

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

type TCPChecker struct {
	Pool           *pool.Pool
	Interval       time.Duration
	Timeout        time.Duration
	UnhealthyTresh int
	HealthyThresh  int
	Logger         *slog.Logger
}

func (c *TCPChecker) Run(ctx context.Context) {
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

func (c *TCPChecker) checkOne(b *pool.Backend) {
	before := b.IsHealthy()

	conn, err := net.DialTimeout("tcp", b.Addr, c.Timeout)
	if err != nil {
		b.RecordFailure(c.UnhealthyTresh)
	} else {
		conn.Close()
		b.RecordSuccess(c.HealthyThresh)
	}

	if after := b.IsHealthy(); after != before {
		c.Logger.Info("backend health changed", "backend", b.Addr, "healthy", after)
	}
}
