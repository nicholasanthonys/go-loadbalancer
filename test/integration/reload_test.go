package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholasanthonys/gobalance/internal/balancer"
	"github.com/nicholasanthonys/gobalance/internal/pool"
	"github.com/nicholasanthonys/gobalance/internal/proxy"
)

// newBackend spins up an httptest backend that identifies itself by id and
// returns its host:port address (what the pool/proxy expect).
func newBackend(t *testing.T, id string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "backend-%s", id)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// TestHotReloadDuringTraffic is the core hot-reload guarantee: swapping a
// pool's backend set and weights while requests are in flight must not drop a
// single request, and must not trip the race detector. Run with -race.
//
// addr0 and addr1 are present in every backend set, so SetBackends reuses
// their *Backend (and their healthy state) on each reload — there is always a
// healthy backend to serve traffic. addr2 comes and goes; when it is removed
// and later re-added it returns as a fresh unhealthy backend, which simply
// means it receives no traffic until its (not-running-here) health checks pass.
func TestHotReloadDuringTraffic(t *testing.T) {
	addr0 := newBackend(t, "0")
	addr1 := newBackend(t, "1")
	addr2 := newBackend(t, "2")

	p := pool.New([]string{addr0, addr1, addr2})
	for _, b := range p.All() {
		b.RecordSuccess(1) // mark healthy; the health checker is tested elsewhere
	}

	// weighted_round_robin reads Backend.Weight() on every Pick(); the reloader
	// below calls SetWeight() concurrently. That read/write pair is exactly
	// what this test puts under -race.
	bal, err := balancer.New("weighted_round_robin", p)
	if err != nil {
		t.Fatal(err)
	}

	lb := httptest.NewServer(proxy.NewL7Handler(bal))
	t.Cleanup(lb.Close)

	var (
		wg   sync.WaitGroup
		ok   atomic.Int64
		bad  atomic.Int64
		stop = make(chan struct{})
	)

	// Traffic generators.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 2 * time.Second}
			for {
				select {
				case <-stop:
					return
				default:
				}
				resp, err := client.Get(lb.URL)
				if err != nil {
					bad.Add(1)
					continue
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					ok.Add(1)
				} else {
					bad.Add(1)
				}
			}
		}()
	}

	// Reloader: churn the backend set and weights while traffic flows.
	wg.Add(1)
	go func() {
		defer wg.Done()
		sets := [][]pool.BackendSpec{
			{{Addr: addr0, Weight: 1}, {Addr: addr1, Weight: 1}, {Addr: addr2, Weight: 1}},
			{{Addr: addr0, Weight: 5}, {Addr: addr1, Weight: 2}},
			{{Addr: addr0, Weight: 1}, {Addr: addr1, Weight: 9}, {Addr: addr2, Weight: 3}},
		}
		for i := 0; i < 300; i++ {
			select {
			case <-stop:
				return
			default:
			}
			p.SetBackends(sets[i%len(sets)])
			time.Sleep(time.Millisecond)
		}
	}()

	time.Sleep(400 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Logf("requests during reload churn: %d ok, %d failed", ok.Load(), bad.Load())
	if ok.Load() == 0 {
		t.Fatal("no successful requests — traffic never flowed, test is not exercising anything")
	}
	if bad.Load() != 0 {
		t.Fatalf("hot reload dropped %d in-flight request(s); reload must be transparent to traffic", bad.Load())
	}
}
