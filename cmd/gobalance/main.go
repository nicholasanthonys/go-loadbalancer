package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/nicholasanthonys/gobalance/internal/balancer"
	"github.com/nicholasanthonys/gobalance/internal/healthcheck"
	"github.com/nicholasanthonys/gobalance/internal/pool"
	"github.com/nicholasanthonys/gobalance/internal/proxy"
)

func main() {
	fmt.Println("gobalance starting...")
	var enableTLS = flag.Bool("tls", true, "terminate TLS at both listeners")

	flag.Parse()

	p := pool.New([]string{"localhost:9101", "localhost:9102", "localhost:9103"})
	fmt.Println("Backends initialized:", len(p.Backends))

	rr := balancer.NewRoundRobin(p)

	TCPChecker := &healthcheck.TCPChecker{
		Pool:           p,
		Interval:       5 * time.Second,
		Timeout:        2 * time.Second,
		UnhealthyTresh: 3,
		HealthyThresh:  2,
	}
	go TCPChecker.Run(context.Background())

	var tlsConfig *tls.Config
	if *enableTLS {
		cert, err := tls.LoadX509KeyPair("certs/cert.pem", "certs/key.pem")
		if err != nil {
			fmt.Println("Error loading TLS certificate:", err)
			return
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}

	go func() {
		var err error
		if *enableTLS {
			fmt.Println("L7 listening on :9091 (TLS)")
			err = http.ListenAndServeTLS(":9091", "certs/cert.pem", "certs/key.pem", proxy.NewL7Handler(rr))
		} else {
			fmt.Println("L7 listening on :9091 (plaintext)")
			err = http.ListenAndServe(":9091", proxy.NewL7Handler(rr))
		}
		if err != nil {
			fmt.Println("Error starting L7 proxy:", err)
		}
	}()

	fmt.Println("L4 listening on :9090")
	if err := proxy.ServeL4("localhost:9090", rr, tlsConfig); err != nil {
		fmt.Println("Error starting L4 proxy:", err)
	}
}
