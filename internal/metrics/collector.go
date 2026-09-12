package metrics

import (
	"github.com/nicholasanthonys/gobalance/internal/pool"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	activeConnsDesc = prometheus.NewDesc(
		"gobalance_backend_active_connections", "Current active connections per backend.",
		[]string{"listener", "backend"}, nil,
	)
	backendHealthyDesc = prometheus.NewDesc(
		"gobalance_backend_healthy", "1 if the backend is currently healthy, else 0.",
		[]string{"listener", "backend"}, nil,
	)
)

// BackendCollector reads live state off each listener's pool at scrape
// time instead of caching it. Pools is the same listener-name -> *pool.Pool
// registry main.go already builds at startup; it's read-only after startup,
// so sharing it here needs no extra locking.
type BackendCollector struct {
	Pools map[string]*pool.Pool
}

func (c *BackendCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- activeConnsDesc
	ch <- backendHealthyDesc
}

func (c *BackendCollector) Collect(ch chan<- prometheus.Metric) {
	for listener, p := range c.Pools {
		for _, b := range p.All() {
			ch <- prometheus.MustNewConstMetric(activeConnsDesc, prometheus.GaugeValue, float64(b.ActiveConns()), listener, b.Addr)
			healthy := 0.0
			if b.IsHealthy() {
				healthy = 1
			}
			ch <- prometheus.MustNewConstMetric(backendHealthyDesc, prometheus.GaugeValue, healthy, listener, b.Addr)
		}
	}

}
