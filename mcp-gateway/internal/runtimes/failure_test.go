package runtimes

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
)

func TestClassifyFailureClosedTable(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		state     contract.RuntimeState
		reason    contract.PublicReason
		retryable bool
		lost      bool
	}{
		{name: "owner capacity", err: ErrRuntimeOwnerLimit, state: contract.RuntimeDegraded, reason: contract.ReasonResourceLimit, retryable: true},
		{name: "material", err: ErrMaterialLease, state: contract.RuntimeDegraded, reason: contract.ReasonConfigurationInvalid},
		{name: "desired", err: servers.ErrInvalidInput, state: contract.RuntimeDegraded, reason: contract.ReasonConfigurationInvalid},
		{name: "URL", err: remote.ErrInvalidURL, state: contract.RuntimeDegraded, reason: contract.ReasonConfigurationInvalid},
		{name: "address policy", err: remote.ErrAddressPolicy, state: contract.RuntimeDegraded, reason: contract.ReasonConfigurationInvalid},
		{name: "redirect", err: remote.ErrRedirect, state: contract.RuntimeDegraded, reason: contract.ReasonConfigurationInvalid},
		{name: "unsupported", err: downstream.ErrUnsupportedProtocol, state: contract.RuntimeDegraded, reason: contract.ReasonProtocolUnsupported, lost: true},
		{name: "fallback rejected", err: downstream.ErrFallbackRejected, state: contract.RuntimeDegraded, reason: contract.ReasonProtocolUnsupported, lost: true},
		{name: "invalid wire", err: downstream.ErrInvalidMessage, state: contract.RuntimeDegraded, reason: contract.ReasonProtocolInvalid, retryable: true, lost: true},
		{name: "mismatched response", err: downstream.ErrResponseMismatch, state: contract.RuntimeDegraded, reason: contract.ReasonProtocolInvalid, retryable: true, lost: true},
		{name: "session", err: downstream.ErrSessionLost, state: contract.RuntimeDegraded, reason: contract.ReasonConnectivity, retryable: true, lost: true},
		{name: "transport", err: downstream.ErrTransportClosed, state: contract.RuntimeDegraded, reason: contract.ReasonConnectivity, retryable: true, lost: true},
		{name: "remote status", err: downstream.ErrRemoteUnavailable, state: contract.RuntimeDegraded, reason: contract.ReasonConnectivity, retryable: true, lost: true},
		{name: "authentication", err: downstream.ErrAuthenticationRejected, state: contract.RuntimeAuthenticationRequired, reason: contract.ReasonAuthenticationRejected, lost: true},
		{name: "response limit", err: remote.ErrResponseLimit, state: contract.RuntimeDegraded, reason: contract.ReasonOutputLimit, retryable: true, lost: true},
		{name: "TLS", err: x509.HostnameError{Certificate: &x509.Certificate{}, Host: "wrong.example"}, state: contract.RuntimeDegraded, reason: contract.ReasonTLSFailed, retryable: true, lost: true},
		{name: "cancelled", err: context.Canceled, state: contract.RuntimeDegraded, reason: contract.ReasonCancelled},
		{name: "stop", err: downstream.ErrStopUnconfirmed, state: contract.RuntimeDegraded, reason: contract.ReasonStopUnconfirmed},
		{name: "connectivity", err: errors.New("dial failed"), state: contract.RuntimeDegraded, reason: contract.ReasonConnectivity, retryable: true, lost: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			disposition := ClassifyFailure(test.err)
			assert.Equal(t, test.state, disposition.State)
			assert.Equal(t, test.reason, disposition.Reason)
			assert.Equal(t, test.retryable, disposition.Retryable)
			assert.Equal(t, test.lost, disposition.RuntimeLost)
		})
	}
}
