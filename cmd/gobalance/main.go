package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/nicholasanthonys/gobalance/internal/balancer"
	"github.com/nicholasanthonys/gobalance/internal/config"
	"github.com/nicholasanthonys/gobalance/internal/healthcheck"
	"github.com/nicholasanthonys/gobalance/internal/logging"
	"github.com/nicholasanthonys/gobalance/internal/metrics"
	"github.com/nicholasanthonys/gobalance/internal/pool"
	"github.com/nicholasanthonys/gobalance/internal/proxy"
)

var metricsAddr = flag.String("metrics-addr", ":9100", "address for the /metrics endpoint")

// shutdownableServer is satisfied by both *http.Server (L7 listeners, the
// metrics server) and *proxy.L4Server (L4 listeners) so main can drain every
// listener the same way on SIGTERM/SIGINT.
type shutdownableServer interface {
	Shutdown(ctx context.Context) error
}

func main() {
	fmt.Println("gobalance starting...")
	logger := logging.New()
	var enableTLS = flag.Bool("tls", true, "terminate TLS at both listeners")
	var configPath = flag.String("config", "configs/example.yaml", "path to config file")
	var shutdownTimeout = flag.Duration("shutdown-timeout", 30*time.Second, "max time to wait for in-flight connections to drain on shutdown")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Println("Error loading config:", err)
		return
	}
	store := config.NewStore(cfg)
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
	pools := make(map[string]*pool.Pool, len(store.Get().Listeners))
	started := make(map[string]config.Listener, len(store.Get().Listeners))
	for _, l := range store.Get().Listeners {
		pools[l.Name] = buildPool(l)
		started[l.Name] = l
	}

	reload := func() {
		if err := store.Reload(*configPath); err != nil {
			metrics.ReloadsTotal.WithLabelValues("failure").Inc()
			logger.Warn("reload failed, keeping old config", "error", err)
			return
		}
		metrics.ReloadsTotal.WithLabelValues("success").Inc()
		logger.Info("reload applied")
		applyConfig(store.Get(), pools, started)
	}

	// servers holds every listener's shutdown handle so a SIGTERM/SIGINT can
	// drain all of them together at the end of main.
	var servers []shutdownableServer
	for _, l := range store.Get().Listeners {
		p := pools[l.Name]
		fmt.Printf("listener %q (%s) starting on %s\n", l.Name, l.Type, l.Listen)
		srv, err := startListener(l, p, tlsConfig, logger)
		if err != nil {
			fmt.Printf("listener %q failed to start: %v\n", l.Name, err)
			continue
		}
		servers = append(servers, srv)
	}

	prometheus.MustRegister(&metrics.BackendCollector{Pools: pools})
	metricsSrv := &http.Server{Addr: *metricsAddr, Handler: promhttp.Handler()}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server stopped", "error", err)
		}
	}()
	servers = append(servers, metricsSrv)

	// Reload trigger 1: SIGHUP. `kill -HUP <pid>` re-reads the config file.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	go func() {
		for range sigCh {
			log.Println("reload: SIGHUP received")
			reload()
		}
	}()

	// Reload trigger 2: config file changes on disk. We watch the containing
	// directory, not the file itself: editors save by writing a temp file and
	// renaming it over the original, which invalidates a watch held directly on
	// the file. One save often emits several events, so we debounce — reset a
	// 200ms timer on every relevant event and only call reload() once it fires.
	if watcher, err := fsnotify.NewWatcher(); err != nil {
		log.Printf("reload: file watching disabled: %v", err)
	} else if err := watcher.Add(filepath.Dir(*configPath)); err != nil {
		log.Printf("reload: file watching disabled: %v", err)
		watcher.Close()
	} else {
		go func() {
			defer watcher.Close()
			var debounce *time.Timer
			for {
				select {
				case event, ok := <-watcher.Events:
					if !ok {
						return
					}
					if filepath.Clean(event.Name) != filepath.Clean(*configPath) {
						continue
					}
					if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
						continue
					}
					if debounce != nil {
						debounce.Stop()
					}
					debounce = time.AfterFunc(200*time.Millisecond, reload)
				case err, ok := <-watcher.Errors:
					if !ok {
						return
					}
					log.Printf("reload: watcher error: %v", err)
				}
			}
		}()
	}

	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, syscall.SIGTERM, syscall.SIGINT)
	<-shutdownCh
	logger.Info("shutdown signal received, draining", "timeout", shutdownTimeout.String())

	ctx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, srv := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.Shutdown(ctx); err != nil {
				logger.Warn("listener did not shut down cleanly", "error", err)
			}
		}()
	}
	wg.Wait()
	logger.Info("shutdown complete")
}

// applyConfig takes a freshly-reloaded config and pushes whatever changes
// are safe to apply live (backend list + weights) onto the running pools.
// Algorithm/type changes are logged and ignored — those need a restart.

func applyConfig(newCfg *config.Config, pools map[string]*pool.Pool, started map[string]config.Listener) {
	for _, l := range newCfg.Listeners { // <-- for each listener in the new confrig
		p, ok := pools[l.Name] // <-- look up the pool for that listener by name
		if !ok {
			log.Printf("reload: listener %q is new; adding listeners needs a restart, skipping", l.Name)
			continue
		}

		old := started[l.Name] // <-- look up the listener that was started for that pool
		if l.Algorithm != old.Algorithm || l.Type != old.Type {
			log.Printf("reload: listener %q algorithm/type change needs a restart, ignoring that part", l.Name)
		}

		specs := make([]pool.BackendSpec, 0, len(l.Backends))
		for _, b := range l.Backends {
			specs = append(specs, pool.BackendSpec{Addr: b.Addr, Weight: b.Weight})
		}
		p.SetBackends(specs)
	}
}

// buildPool constructs the *pool.Pool for a single config.Listener from its
// backend list.
func buildPool(l config.Listener) *pool.Pool {
	specs := make([]pool.BackendSpec, 0, len(l.Backends))
	for _, b := range l.Backends {
		specs = append(specs, pool.BackendSpec{Addr: b.Addr, Weight: b.Weight})
	}

	p := pool.New(nil)
	p.SetBackends(specs)
	return p
}

// startListener wires up the balancer and health-checker for a single
// config.Listener around an already-built *pool.Pool, starts serving it in
// the background, and returns a handle main can call Shutdown on. Only
// startup errors (bad algorithm, unknown listener type) are returned
// synchronously; errors that occur once serving is underway are logged from
// inside the background goroutine instead.
func startListener(l config.Listener, p *pool.Pool, tlsConfig *tls.Config, logger *slog.Logger) (shutdownableServer, error) {
	bal, err := balancer.New(l.Algorithm, p)
	if err != nil {
		return nil, fmt.Errorf("listener %q: %w", l.Name, err)
	}

	switch l.Type {
	case "l4":
		check := &healthcheck.TCPChecker{
			Pool:           p,
			Interval:       l.HealthCheck.Interval.Duration,
			Timeout:        l.HealthCheck.Timeout.Duration,
			UnhealthyTresh: l.HealthCheck.UnhealthyThreshold,
			HealthyThresh:  l.HealthCheck.HealthyThreshold,
			Logger:         logger,
		}
		go check.Run(context.Background())

		srv := &proxy.L4Server{Balancer: bal, Logger: logger, ListenerName: l.Name}
		go func() {
			if err := srv.ListenAndServe(l.Listen, tlsConfig); err != nil {
				logger.Error("l4 listener stopped", "listener", l.Name, "error", err)
			}
		}()
		return srv, nil

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
			Logger:         logger,
		}
		go check.Run(context.Background())

		handler := proxy.NewL7Handler(bal, logger, l.Name)
		srv := &http.Server{Addr: l.Listen, Handler: handler}
		go func() {
			var err error
			if tlsConfig != nil {
				srv.TLSConfig = tlsConfig
				err = srv.ListenAndServeTLS("certs/cert.pem", "certs/key.pem")
			} else {
				err = srv.ListenAndServe()
			}
			if err != nil && err != http.ErrServerClosed {
				logger.Error("l7 listener stopped", "listener", l.Name, "error", err)
			}
		}()
		return srv, nil

	default:
		return nil, fmt.Errorf("listener %q: unknown type %q", l.Name, l.Type)
	}
}
