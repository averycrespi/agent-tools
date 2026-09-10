package oauth

import (
	"context"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

// The lifecycle test joins real FlowService installation to real Manager
// reconciliation. Only persistence/protocol seams are fixtures; auth readiness
// follows the token store actually written by the callback, not a canned event.
type oauthDiagnosticRuntimeRepository struct {
	runtimes.Repository
	serverID string
}

func (repository *oauthDiagnosticRuntimeRepository) Get(context.Context, string) (servers.Server, error) {
	return servers.Server{ID: repository.serverID, DesiredState: contract.DesiredServerEnabled, DesiredRevision: "1", Transport: []byte(`{"kind":"stdio","executable":"/fixture/mcp","arguments":[],"working_directory":"/","environment":{},"secret_environment":{}}`)}, nil
}
func (*oauthDiagnosticRuntimeRepository) Authority(context.Context, string) (servers.AuthorityMetadata, error) {
	return servers.AuthorityMetadata{}, nil
}
func (*oauthDiagnosticRuntimeRepository) NewID() (string, error) {
	return "01ARZ3NDEKTSV4RRFFQ69G5FAV", nil
}
func (*oauthDiagnosticRuntimeRepository) RecordReconciliation(context.Context, contract.AuditEvent) error {
	return nil
}

type oauthDiagnosticAuthority struct{ secrets *tokenSecrets }

func (authority oauthDiagnosticAuthority) Resolve(context.Context, runtimes.Candidate) runtimes.AuthorityOutcome {
	authority.secrets.mu.Lock()
	defer authority.secrets.mu.Unlock()
	if len(authority.secrets.secret) != 0 {
		return runtimes.AuthorityOutcome{CredentialState: contract.ServerCredentialReady}
	}
	reason := contract.ReasonCredentialAbsent
	return runtimes.AuthorityOutcome{State: contract.RuntimeAuthenticationRequired, CredentialState: contract.ServerCredentialUnavailable, Reason: &reason}
}

type oauthDiagnosticDriver struct{}

func (oauthDiagnosticDriver) Reconcile(context.Context, runtimes.Candidate, *runtimes.MaterialLease) runtimes.Outcome {
	return runtimes.Outcome{State: contract.RuntimeActive, CredentialState: contract.ServerCredentialReady}
}
func (oauthDiagnosticDriver) Stop(context.Context, runtimes.Candidate) bool { return true }

type oauthDiagnosticCatalog struct{}

func (oauthDiagnosticCatalog) Activate(context.Context, runtimes.Candidate) runtimes.CatalogOutcome {
	return runtimes.CatalogOutcome{State: contract.ActiveCatalogCurrent}
}
