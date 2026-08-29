package proxy

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/nicholasanthonys/gobalance/internal/balancer"
)

type pickErrKey struct{}

func NewL7Handler(b balancer.Balancer) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			backend, err := b.Pick()
			if err != nil {
				ctx := context.WithValue(r.Out.Context(), pickErrKey{}, err)
				r.Out = r.Out.WithContext(ctx)
				return
			}
			r.SetURL(&url.URL{
				Scheme: "http",
				Host:   backend.Addr,
			})
			r.SetXForwarded()
		},
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if err, ok := req.Context().Value(pickErrKey{}).(error); ok {
				return nil, err
			}
			return http.DefaultTransport.RoundTrip(req)
		}),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		},
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
