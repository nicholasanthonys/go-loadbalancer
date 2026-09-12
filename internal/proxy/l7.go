package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"github.com/nicholasanthonys/gobalance/internal/balancer"
	"github.com/nicholasanthonys/gobalance/internal/metrics"
)

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

type pickErrKey struct{}

func NewL7Handler(b balancer.Balancer, logger *slog.Logger, listenerName string) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			backend, err := b.Pick()
			if err != nil {
				ctx := context.WithValue(r.Out.Context(), pickErrKey{}, err)
				r.Out = r.Out.WithContext(ctx)
				return
			}
			r.SetURL(&url.URL{
				Scheme: "http",
				Host:   backend.Addr,
			})
			r.SetXForwarded()
			metrics.BackendRequestsTotal.WithLabelValues(listenerName, backend.Addr).Inc()
		},
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if err, ok := req.Context().Value(pickErrKey{}).(error); ok {
				return nil, err
			}
			return http.DefaultTransport.RoundTrip(req)
		}),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK} // WriteHeader(200) is implicit if never called
		rp.ServeHTTP(sw, r)
		status := strconv.Itoa(sw.status)
		metrics.RequestsTotal.WithLabelValues(listenerName, status).Inc()
		metrics.RequestDuration.WithLabelValues(listenerName).Observe(time.Since(start).Seconds())
		logger.Info("l7 request served", "listener", listenerName, "status", sw.status, "duration_ms", time.Since(start).Milliseconds())
	})

}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
