package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/nicholasanthonys/gobalance/internal/balancer"
	"github.com/nicholasanthonys/gobalance/internal/metrics"
)

// L4Server runs a raw-TCP proxy accept loop and supports graceful shutdown:
// Shutdown stops Accept from picking up new connections, then waits for
// already-accepted connections to finish their io.Copy pumps (or for the
// context to expire, whichever comes first). Its Shutdown signature matches
// http.Server's so both listener types can be drained the same way from main.
type L4Server struct {
	Balancer     balancer.Balancer
	Logger       *slog.Logger
	ListenerName string

	mu     sync.Mutex
	ln     net.Listener
	closed bool
	wg     sync.WaitGroup
}

// ListenAndServe opens a TCP (or TLS, if tlsConfig is non-nil) listener on
// addr and runs the accept loop. It blocks until Shutdown closes the
// listener, at which point it returns nil.
func (s *L4Server) ListenAndServe(addr string, tlsConfig *tls.Config) error {
	var ln net.Listener
	var err error
	if tlsConfig != nil {
		ln, err = tls.Listen("tcp", addr, tlsConfig)
	} else {
		ln, err = net.Listen("tcp", addr)
	}
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve runs the accept loop on an already-open listener. It blocks until
// Shutdown closes the listener, at which point it returns nil.
func (s *L4Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			continue // transient accept error; keep serving
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

// Shutdown stops accepting new connections and waits for in-flight ones to
// finish their io.Copy pumps, up to ctx's deadline.
func (s *L4Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	ln := s.ln
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *L4Server) handleConn(client net.Conn) {
	start := time.Now()
	defer client.Close()
	backend, err := s.Balancer.Pick()
	if err != nil {
		metrics.RequestsTotal.WithLabelValues(s.ListenerName, "error").Inc()
		return
	}
	backend.IncActiveConns()
	defer backend.DecActiveConns()
	metrics.BackendRequestsTotal.WithLabelValues(s.ListenerName, backend.Addr).Inc()

	upstream, err := net.DialTimeout("tcp", backend.Addr, 5*time.Second)
	if err != nil {
		metrics.RequestsTotal.WithLabelValues(s.ListenerName, "error").Inc()
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

	metrics.RequestsTotal.WithLabelValues(s.ListenerName, "ok").Inc()
	metrics.RequestDuration.WithLabelValues(s.ListenerName).Observe(time.Since(start).Seconds())
	s.Logger.Info("l4 connection served", "listener", s.ListenerName, "backend", backend.Addr, "duration_ms", time.Since(start).Milliseconds())
}
