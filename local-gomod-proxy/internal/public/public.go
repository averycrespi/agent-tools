package public

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// Fetcher reverse-proxies public module requests to a Go module proxy
// (typically proxy.golang.org).
type Fetcher struct {
	proxy *httputil.ReverseProxy
}

// New returns a Fetcher targeting the given upstream URL.
func New(upstream *url.URL) *Fetcher {
	rp := httputil.NewSingleHostReverseProxy(upstream)
	orig := rp.Director
	rp.Director = func(r *http.Request) {
		orig(r)
		// Never leak our inbound auth to the upstream.
		r.Header.Del("Authorization")
		// Ensure Host matches the upstream.
		r.Host = upstream.Host
	}
	return &Fetcher{proxy: rp}
}

// ServeHTTP implements http.Handler.
func (f *Fetcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.proxy.ServeHTTP(w, r)
}
