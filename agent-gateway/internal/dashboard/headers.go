package dashboard

import "net/http"

// cspValue is the Content-Security-Policy applied to every dashboard HTML / API
// response. The directives are tight on purpose:
//
//   - default-src 'none' — nothing loads unless explicitly allowlisted below.
//   - script-src 'self' — scripts only from the same origin; NO 'unsafe-inline'.
//     This is load-bearing: inline onclick= handlers and inline <script> blocks
//     would be blocked outright. Task 4.1 of the security remediation rewrote
//     every inline handler to addEventListener and every innerHTML assignment
//     to safe DOM construction specifically so this CSP is achievable. If you
//     find yourself tempted to add 'unsafe-inline' here, you are instead
//     reintroducing the XSS surface 4.1 closed — fix the offending HTML/JS.
//   - style-src 'self' 'unsafe-inline' — we use a handful of inline style
//     attributes (e.g. on the /dashboard/unauthorized form) and a <style> tag
//     in the same page. 'unsafe-inline' for styles is a materially smaller
//     risk than for scripts (no code execution, only visual spoofing), so
//     pragmatically allowed.
//   - img-src 'self' data: — same-origin plus data: URLs so favicon.svg and
//     any small embedded previews work.
//   - connect-src 'self' — fetch()/XHR/EventSource only to same origin. The
//     SSE feed at /dashboard/api/events and all /dashboard/api/* JSON calls
//     qualify; any future attempt to exfiltrate data to a remote origin via
//     the dashboard's JS would be blocked by the browser.
//   - frame-ancestors 'none' — the authoritative clickjacking defence; also
//     backed up with X-Frame-Options: DENY for older browsers.
const cspValue = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"frame-ancestors 'none'"

// secureHeaders wraps next, stamping CSP + hardening headers on every response
// before delegating to the downstream handler.
//
// /ca.pem is special-cased: a CSP on a PEM download is nonsensical (the browser
// hands the bytes to the OS cert installer, not a rendering context) and could
// in theory confuse a very old client, so CSP is skipped for that path. The
// other three headers (nosniff / frame / referrer) are harmless on a cert
// download and still apply — belt-and-braces against any client that does
// attempt to render the response inline.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if r.URL.Path != "/ca.pem" {
			h.Set("Content-Security-Policy", cspValue)
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
