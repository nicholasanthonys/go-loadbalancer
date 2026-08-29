package proxy

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/nicholasanthonys/gobalance/internal/balancer"
)

func ServeL4(listenAddr string, b balancer.Balancer) error {

	// open a TCP listener to accept incoming connections on the specified address
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}

	// open a loop to accept incoming connections and handle them concurrently
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue // log and keep serving
		}
		go handleConn(conn, b)
	}
}

func handleConn(client net.Conn, b balancer.Balancer) {
	defer client.Close()
	backend, err := b.Pick()
	if err != nil {
		return
	}
	backend.IncActiveConns()
	defer backend.DecActiveConns()

	upstream, err := net.DialTimeout("tcp", backend.Addr, 5*time.Second)
	if err != nil {
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
}
