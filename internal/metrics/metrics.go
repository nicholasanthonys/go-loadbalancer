package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	// Define your metrics here
	RequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gobalance_requests_total",
			Help: "Total number of requests handled, by listener and outcome0",
		},
		[]string{"listener", "status"},
	)

	RequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{Name: "gobalance_request_duration_seconds", Help: "Request/connection duration by listener."},
		[]string{"listener"},
	)
	ReloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "gobalance_reloads_total", Help: "Config reload attempts, by result."},
		[]string{"result"}, // "success" or "failure"
	)
)

func init() {
	prometheus.MustRegister(RequestsTotal, RequestDuration, ReloadsTotal)
}
