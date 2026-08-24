package runtimes

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

type FailureDisposition struct {
	State       contract.RuntimeState
	Reason      contract.PublicReason
	Retryable   bool
	RuntimeLost bool
}

func ClassifyFailure(err error) FailureDisposition {
	disposition := FailureDisposition{State: contract.RuntimeDegraded, Reason: contract.ReasonConnectivity, Retryable: true, RuntimeLost: true}
	switch {
	case errors.Is(err, ErrRuntimeOwnerLimit):
		disposition.Reason = contract.ReasonResourceLimit
		disposition.RuntimeLost = false
	case errors.Is(err, ErrMaterialLease), errors.Is(err, servers.ErrInvalidInput), errors.Is(err, remote.ErrInvalidURL), errors.Is(err, remote.ErrAddressPolicy), errors.Is(err, remote.ErrRedirect):
		disposition.Reason = contract.ReasonConfigurationInvalid
		disposition.Retryable = false
		disposition.RuntimeLost = false
	case errors.Is(err, downstream.ErrUnsupportedProtocol), errors.Is(err, downstream.ErrFallbackRejected):
		disposition.Reason = contract.ReasonProtocolUnsupported
		disposition.Retryable = false
	case errors.Is(err, downstream.ErrInvalidMessage), errors.Is(err, downstream.ErrResponseMismatch):
		disposition.Reason = contract.ReasonProtocolInvalid
	case errors.Is(err, downstream.ErrAuthenticationRejected):
		disposition.State = contract.RuntimeAuthenticationRequired
		disposition.Reason = contract.ReasonAuthenticationRejected
		disposition.Retryable = false
	case errors.Is(err, remote.ErrResponseLimit):
		disposition.Reason = contract.ReasonOutputLimit
	case tlsFailure(err):
		disposition.Reason = contract.ReasonTLSFailed
	case errors.Is(err, context.Canceled):
		disposition.Reason = contract.ReasonCancelled
		disposition.Retryable = false
		disposition.RuntimeLost = false
	case errors.Is(err, downstream.ErrStopUnconfirmed):
		disposition.Reason = contract.ReasonStopUnconfirmed
		disposition.Retryable = false
		disposition.RuntimeLost = false
	}
	return disposition
}

func validRuntimeFailure(failure FailureDisposition) bool {
	if !failure.RuntimeLost {
		return false
	}
	if _, err := contract.ParsePublicReason(string(failure.Reason)); err != nil {
		return false
	}
	switch failure.State {
	case contract.RuntimeDegraded:
		return failure.Reason != contract.ReasonStopUnconfirmed && failure.Reason != contract.ReasonCancelled
	case contract.RuntimeAuthenticationRequired:
		return failure.Reason == contract.ReasonAuthenticationRejected && !failure.Retryable
	default:
		return false
	}
}

func tlsFailure(err error) bool {
	var certificateVerification *tls.CertificateVerificationError
	var hostname x509.HostnameError
	var unknownAuthority x509.UnknownAuthorityError
	var certificateInvalid x509.CertificateInvalidError
	var recordHeader tls.RecordHeaderError
	var alert tls.AlertError
	return errors.As(err, &certificateVerification) || errors.As(err, &hostname) || errors.As(err, &unknownAuthority) || errors.As(err, &certificateInvalid) || errors.As(err, &recordHeader) || errors.As(err, &alert)
}
