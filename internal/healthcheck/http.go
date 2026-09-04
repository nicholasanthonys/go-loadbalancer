package healthcheck

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

type HTTPChecker struct {
	Path           string
	ExpectedStatus int
	HTTPClient     *http.Client
	Pool           *pool.Pool
	Interval       time.Duration
	Timeout        time.Duration
	UnhealthyTresh int
	HealthyThresh  int
}

func (c *HTTPChecker) Run(ctx context.Context) {
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

func (c *HTTPChecker) checkOne(b *pool.Backend) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	target := url.URL{Scheme: "http", Host: b.Addr, Path: c.Path}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		b.RecordFailure(c.UnhealthyTresh)
		return
	}
	res, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		b.RecordFailure(c.UnhealthyTresh)
		return
	}
	defer res.Body.Close()

	if res.StatusCode != c.ExpectedStatus {
		b.RecordFailure(c.UnhealthyTresh)
		return
	}
	b.RecordSuccess(c.HealthyThresh)
}
