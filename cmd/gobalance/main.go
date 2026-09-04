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

	for _, l := range cfg.Listeners {
		go func() {
			fmt.Printf("listener %q (%s) starting on %s\n", l.Name, l.Type, l.Listen)
			if err := startListener(l, tlsConfig); err != nil {
				fmt.Printf("listener %q stopped: %v\n", l.Name, err)
			}
		}()
	}

	select {} // keep main alive; real graceful shutdown comes in Phase 8
}

// startListener builds the pool/balancer/health-checker for a single
// config.Listener and then starts serving it. It blocks for as long as
// the listener is running and returns the error that stopped it.
func startListener(l config.Listener, tlsConfig *tls.Config) error {
	var addrs []string
	for _, backend := range l.Backends {
		addrs = append(addrs, backend.Addr)
	}

	p := pool.New(addrs)
	for i := range l.Backends {
		p.Backends[i].Weight = l.Backends[i].Weight
	}

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
