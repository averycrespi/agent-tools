package proxy

import "net/http"

// HandleForTest exposes the internal handle method for white-box testing of
// the pipeline verdict dispatch without requiring a full TLS connection.
func (p *Proxy) HandleForTest(w http.ResponseWriter, r *http.Request, host string) {
	p.handle(w, r, host)
}
