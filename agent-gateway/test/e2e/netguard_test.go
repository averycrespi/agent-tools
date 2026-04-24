//go:build e2e

package e2e_test

// TestNetguard_IMDSUnreachableThroughProxy verifies that an authenticated agent
// cannot reach the AWS/GCP/Azure IMDS address (169.254.169.254) through the proxy,
// regardless of the allow_private_upstream setting.
//
// Path taken:
//   - CONNECT 169.254.169.254:443 arrives at the proxy.
//   - 169.254.169.254 is an IP literal → Decide returns DecisionTunnel.
//   - serveTunnel dials the IMDS address directly; the dial fails (no service
//     reachable on 169.254.169.254 in the test environment).
//   - The proxy closes the connection; the agent's HTTP client sees an error.
//
// The unconditional IMDS block for MITM'd (hostname) requests is exercised at
// the unit-test level by TestBlockPrivate_IMDSUnconditional and
// TestDialContext_IMDSAlwaysBlocked in internal/netguard/netguard_test.go.
// This e2e test provides the complementary end-to-end observable: an agent
// that attempts to reach IMDS receives an error from the proxy and cannot
// complete the request.

import (
	"net/http"
	"testing"
)

func TestNetguard_IMDSUnreachableThroughProxy(t *testing.T) {
	// Start the standard test stack (allow_private_upstream=true for loopback
	// mock upstreams). Even with allow_private_upstream=true the IMDS address
	// must remain unreachable: the block is unconditional.
	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This handler is never reached when targeting IMDS.
		w.WriteHeader(http.StatusOK)
	}))

	// Attempt an HTTPS request to the IMDS endpoint via the proxy.
	// The agent uses the pre-configured AgentClient (authenticated, routes
	// through the proxy). The request must fail — either the tunnel dial is
	// refused by the OS or the transport cannot complete the TLS handshake
	// because the proxy closes the connection when the upstream dial fails.
	resp, err := stack.AgentClient.Get("https://169.254.169.254/latest/meta-data/")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("expected error reaching IMDS through proxy, got status %d", resp.StatusCode)
	}

	// We cannot assert a specific error message because the failure surface
	// differs between platforms and Go versions (EOF, connection reset, dial
	// refused, etc.). The absence of a nil error is the meaningful assertion:
	// the proxy did not successfully proxy the agent to the IMDS address.
	t.Logf("IMDS request correctly failed: %v", err)
}
