package servers

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type Clock interface {
	Now() time.Time
}

type Definition struct {
	Namespace   string
	DisplayName string
	Enabled     bool
	Transport   contract.Transport
}

type Patch struct {
	DisplayName *string
	Enabled     *bool
	Transport   contract.Transport
}

type Server struct {
	InsertionSequence int64
	ID                string
	Namespace         string
	DisplayName       string
	DesiredState      contract.DesiredServerState
	DesiredRevision   string
	Transport         json.RawMessage
	CreatedAt         string
	UpdatedAt         string
	DeletedAt         *string
}

type IdempotencyRequest struct {
	AuthorityID  string
	Method       string
	Route        string
	Key          string
	RequestHash  [sha256.Size]byte
	Precondition string
}

type IdempotencyResult struct {
	Kind            string
	ServerID        string
	OperationID     *string
	DesiredRevision string
}

type CreateRequest struct {
	ID          string
	Definition  Definition
	Idempotency *IdempotencyRequest
}

type CreateResult struct {
	Server    Server
	Operation *Operation
	Result    IdempotencyResult
	Replayed  bool
}

type PatchResult struct {
	Server
	Operation *Operation
}

type DeleteResult struct {
	Server    Server
	Operation *Operation
	Replayed  bool
}

type AuthorityMetadata struct {
	RegistrationRevision   string
	CredentialRevisions    contract.CredentialRevisions
	StaticCredentialHandle *string
	OAuthClientHandle      *string
	OAuthTokensHandle      *string
}

type CredentialMetadata struct {
	Kind     contract.ServerCredentialKind
	Revision string
	Handle   *string
}

type CredentialFence struct {
	ServerID                     string
	Kind                         contract.ServerCredentialKind
	ExpectedDesiredRevision      string
	ExpectedCredentialRevision   string
	ExpectedRegistrationRevision string
}

type Operation struct {
	InsertionSequence         int64
	ID                        string
	ServerID                  string
	Kind                      contract.ServerOperationKind
	TargetDesiredRevision     string
	TargetCredentialRevisions contract.CredentialRevisions
	State                     contract.ServerOperationState
	Reason                    *contract.PublicReason
	CreatedAt                 string
	StartedAt                 *string
	FinishedAt                *string
}

type OperationTriggerState struct {
	RuntimeState    contract.RuntimeState
	RuntimeReason   *contract.PublicReason
	CredentialState contract.ServerCredentialState
	CatalogState    contract.ActiveCatalogState
}

type OperationRequest struct {
	ID                      string
	ServerID                string
	Kind                    contract.ServerOperationKind
	ExpectedDesiredRevision string
	Idempotency             *IdempotencyRequest
	TriggerState            *OperationTriggerState
}

type OperationResult struct {
	Operation Operation
	Result    IdempotencyResult
	Replayed  bool
}

type SnapshotCursor struct {
	Collection string
	ServerID   string
	Upper      int64
	Floor      int64
	After      int64
	AfterID    string
}

type ServerPage struct {
	Items []Server
	Next  *SnapshotCursor
}

type OperationPage struct {
	Items []Operation
	Next  *SnapshotCursor
}
