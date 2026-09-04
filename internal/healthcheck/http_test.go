package healthcheck

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

func TestHTTPChecker_HysteresisFlipsAtThreshold(t *testing.T) {
	var statusCode atomic.Int32
	statusCode.Store(http.StatusOK)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(statusCode.Load()))
	}))
	defer server.Close()

	b := &pool.Backend{Addr: server.Listener.Addr().String()}
	b.RecordSuccess(1) // seed healthy

	checker := &HTTPChecker{
		Path:           "/healthz",
		ExpectedStatus: http.StatusOK,
		HTTPClient:     &http.Client{},
		Timeout:        time.Second,
		UnhealthyTresh: 3,
		HealthyThresh:  2,
	}

	statusCode.Store(http.StatusInternalServerError)
	checker.checkOne(b)
	checker.checkOne(b)
	if !b.IsHealthy() {
		t.Errorf("expected still healthy after 2 failures, threshold is 3")
	}

	checker.checkOne(b)
	if b.IsHealthy() {
		t.Fatalf("expected unhealthy after 3 consecutive failures")
	}

	statusCode.Store(http.StatusOK)
	checker.checkOne(b)
	if b.IsHealthy() {
		t.Fatalf("expected still unhealthy after 1 success, threshold is 2")
	}

	checker.checkOne(b)
	if !b.IsHealthy() {
		t.Fatalf("expected healthy after 2nd consecutive success")
	}

}
