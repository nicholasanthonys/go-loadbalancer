package main

import (
	"context"
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

	p := pool.New([]string{"localhost:9101", "localhost:9102", "localhost:9103"})
	fmt.Println("Backends initialized:", len(p.Backends))

	rr := balancer.NewRoundRobin(p)

	checker := &healthcheck.Checker{
		Pool:           p,
		Interval:       5 * time.Second,
		Timeout:        2 * time.Second,
		UnhealthyTresh: 3,
		HealthyThresh:  2,
	}
	go checker.Run(context.Background())

	go func() {
		fmt.Println("L7 listening on :9091")
		if err := http.ListenAndServe(":9091", proxy.NewL7Handler(rr)); err != nil {
			fmt.Println("Error starting L7 proxy:", err)
		}
	}()

	fmt.Println("L4 listening on :9090")
	if err := proxy.ServeL4("localhost:9090", rr); err != nil {
		fmt.Println("Error starting L4 proxy:", err)
	}
}
