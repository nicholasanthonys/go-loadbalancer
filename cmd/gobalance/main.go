package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"

	"github.com/nicholasanthonys/gobalance/internal/balancer"
	"github.com/nicholasanthonys/gobalance/internal/config"
	"github.com/nicholasanthonys/gobalance/internal/healthcheck"
	"github.com/nicholasanthonys/gobalance/internal/pool"
	"github.com/nicholasanthonys/gobalance/internal/proxy"
)

func main() {
	fmt.Println("gobalance starting...")
	var enableTLS = flag.Bool("tls", true, "terminate TLS at both listeners")
	var configPath = flag.String("config", "configs/example.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Println("Error loading config:", err)
		return
	}
	fmt.Println("Loaded config:", *configPath, "-", len(cfg.Listeners), "listener(s)")

	var tlsConfig *tls.Config
	if *enableTLS {
		cert, err := tls.LoadX509KeyPair("certs/cert.pem", "certs/key.pem")
		if err != nil {
			fmt.Println("Error loading TLS certificate:", err)
			return
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}

	// pools is a listener-name -> *pool.Pool registry, built once here and
	// never structurally modified afterward (no keys added or removed) — a
	// future config reload looks up a listener's pool by name and calls
	// Pool.SetBackends on it. Because this map is only written during this
	// startup loop, before any reload-watcher goroutine exists, later
	// concurrent reads of the map by a reload goroutine are safe without a
	// lock of their own; the *pool.Pool values it points to already guard
	// their own state internally.
	pools := make(map[string]*pool.Pool, len(cfg.Listeners))
	for _, l := range cfg.Listeners {
		pools[l.Name] = buildPool(l)
	}

	for _, l := range cfg.Listeners {
		p := pools[l.Name]
		go func() {
			fmt.Printf("listener %q (%s) starting on %s\n", l.Name, l.Type, l.Listen)
			if err := startListener(l, p, tlsConfig); err != nil {
				fmt.Printf("listener %q stopped: %v\n", l.Name, err)
			}
		}()
	}

	select {} // keep main alive; real graceful shutdown comes in Phase 8
}

// buildPool constructs the *pool.Pool for a single config.Listener from its
// backend list.
func buildPool(l config.Listener) *pool.Pool {
	var addrs []string
	for _, backend := range l.Backends {
		addrs = append(addrs, backend.Addr)
	}

	p := pool.New(addrs)
	for i := range l.Backends {
		p.Backends[i].Weight = l.Backends[i].Weight
	}
	return p
}

// startListener wires up the balancer and health-checker for a single
// config.Listener around an already-built *pool.Pool, then starts serving
// it. It blocks for as long as the listener is running and returns the
// error that stopped it.
func startListener(l config.Listener, p *pool.Pool, tlsConfig *tls.Config) error {
	bal, err := balancer.New(l.Algorithm, p)
	if err != nil {
		return fmt.Errorf("listener %q: %w", l.Name, err)
	}

	switch l.Type {
	case "l4":
		check := &healthcheck.TCPChecker{
			Pool:           p,
			Interval:       l.HealthCheck.Interval.Duration,
			Timeout:        l.HealthCheck.Timeout.Duration,
			UnhealthyTresh: l.HealthCheck.UnhealthyThreshold,
			HealthyThresh:  l.HealthCheck.HealthyThreshold,
		}
		go check.Run(context.Background())

		return proxy.ServeL4(l.Listen, bal, tlsConfig)

	case "l7":
		check := &healthcheck.HTTPChecker{
			Pool:           p,
			Interval:       l.HealthCheck.Interval.Duration,
			Timeout:        l.HealthCheck.Timeout.Duration,
			UnhealthyTresh: l.HealthCheck.UnhealthyThreshold,
			HealthyThresh:  l.HealthCheck.HealthyThreshold,

			Path:           l.HealthCheck.Path,
			ExpectedStatus: http.StatusOK,
			HTTPClient:     &http.Client{},
		}
		go check.Run(context.Background())

		handler := proxy.NewL7Handler(bal)
		if tlsConfig != nil {
			return http.ListenAndServeTLS(l.Listen, "certs/cert.pem", "certs/key.pem", handler)
		}
		return http.ListenAndServe(l.Listen, handler)

	default:
		return fmt.Errorf("listener %q: unknown type %q", l.Name, l.Type)
	}
}
