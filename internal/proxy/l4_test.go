package proxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/nicholasanthonys/gobalance/internal/pool"
)

// fakeBalancer always picks the one backend it was built with.
type fakeBalancer struct{ addr string }

func (f fakeBalancer) Pick() (*pool.Backend, error) {
	return &pool.Backend{Addr: f.addr}, nil
}

// TestL4Server_ShutdownDrainsInFlightConnections is the Phase 8 checkpoint
// from TUTORIAL.md as an automated test: a slow in-flight connection must
// still complete after Shutdown is called, new connections must be refused
// during the drain window, and Shutdown itself must actually wait for the
// in-flight connection rather than returning as soon as the listener closes.
func TestL4Server_ShutdownDrainsInFlightConnections(t *testing.T) {
	const backendDelay = 300 * time.Millisecond

	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendLn.Close()

	go func() {
		conn, err := backendLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(backendDelay) // stand-in for a slow backend request
		conn.Write([]byte("world"))
	}()

	srv := &L4Server{
		Balancer:     fakeBalancer{addr: backendLn.Addr().String()},
		Logger:       slog.New(slog.DiscardHandler),
		ListenerName: "test",
	}

	frontLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := frontLn.Addr().String()

	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(frontLn) }()

	rawClient, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	client := rawClient.(*net.TCPConn)
	defer client.Close()

	time.Sleep(50 * time.Millisecond) // let Serve accept and spawn handleConn

	client.Write([]byte("hello"))
	client.CloseWrite() // done sending our half; keep reading for the response

	shutdownStart := time.Now()
	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()

	time.Sleep(20 * time.Millisecond) // let Shutdown close the listener
	if conn, err := net.Dial("tcp", addr); err == nil {
		conn.Close()
		t.Error("expected new connection to be refused once Shutdown closed the listener")
	}

	buf := make([]byte, 5)
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("in-flight connection did not complete during drain: %v", err)
	}
	if string(buf) != "world" {
		t.Fatalf("expected backend's delayed response %q, got %q", "world", buf)
	}

	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	if elapsed := time.Since(shutdownStart); elapsed < 200*time.Millisecond {
		t.Errorf("Shutdown returned after %v, expected it to block until the in-flight connection finished (~%v)", elapsed, backendDelay)
	}

	if err := <-serveDone; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}
