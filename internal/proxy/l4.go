package proxy

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/nicholasanthonys/gobalance/internal/balancer"
	"github.com/nicholasanthonys/gobalance/internal/metrics"
)

func ServeL4(listenAddr string, b balancer.Balancer, tlsConfig *tls.Config, logger *slog.Logger, listenerName string) error {
	var ln net.Listener
	var err error
	// open a TCP listener to accept incoming connections on the specified address
	if tlsConfig != nil {
		ln, err = tls.Listen("tcp", listenAddr, tlsConfig)
	} else {
		ln, err = net.Listen("tcp", listenAddr)
	}
	if err != nil {
		return err
	}

	// open a loop to accept incoming connections and handle them concurrently
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue // log and keep serving
		}
		go handleConn(conn, b, logger, listenerName)
	}
}

func handleConn(client net.Conn, b balancer.Balancer, logger *slog.Logger, listenerName string) {

	start := time.Now()
	defer client.Close()
	backend, err := b.Pick()
	if err != nil {
		metrics.RequestsTotal.WithLabelValues(listenerName, "error").Inc()
		return
	}
	backend.IncActiveConns()
	defer backend.DecActiveConns()

	upstream, err := net.DialTimeout("tcp", backend.Addr, 5*time.Second)
	if err != nil {
		metrics.RequestsTotal.WithLabelValues(listenerName, "error").Inc()
		return
	}
	defer upstream.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(upstream, client)
	}()
	go func() {
		defer wg.Done()
		io.Copy(client, upstream)
	}()
	wg.Wait()

	metrics.RequestsTotal.WithLabelValues(listenerName, "ok").Inc()
	metrics.RequestDuration.WithLabelValues(listenerName).Observe(time.Since(start).Seconds())
	logger.Info("l4 connection served", "listener", listenerName, "backend", backend.Addr, "duration_ms", time.Since(start).Milliseconds())
}
