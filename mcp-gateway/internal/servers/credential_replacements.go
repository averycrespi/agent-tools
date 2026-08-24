package servers

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sync"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
)

func (repository *Repository) PrepareCredentialReplacement(ctx context.Context, request CredentialReplacementRequest) (CredentialReplacementPlan, error) {
	if !validID(request.ServerID) {
		return CredentialReplacementPlan{}, ErrNotFound
	}
	if _, err := contract.ParseCredentialReplacementKind(string(request.Kind)); err != nil {
		return CredentialReplacementPlan{}, ErrInvalidOperation
	}
	if _, err := parseRevision(request.ExpectedDesiredRevision); err != nil {
		return CredentialReplacementPlan{}, err
	}
	if _, err := parseRevision(request.ExpectedCredentialRevision); err != nil {
		return CredentialReplacementPlan{}, err
	}
	operationID, err := repository.NewID()
	if err != nil {
		return CredentialReplacementPlan{}, err
	}
	plan := CredentialReplacementPlan{Fence: CredentialFence{
		ServerID: request.ServerID, Kind: request.Kind,
		ExpectedDesiredRevision:    request.ExpectedDesiredRevision,
		ExpectedCredentialRevision: request.ExpectedCredentialRevision,
	}, OperationID: operationID, Slots: append([]string(nil), request.Slots...)}
	err = repository.store.View(ctx, func(transaction *sql.Tx) error {
		registrationRevision, validateErr := validateCredentialReplacementTx(ctx, transaction, plan)
		plan.Fence.ExpectedRegistrationRevision = registrationRevision
		return validateErr
	})
	return plan, mapViewError(err)
}

func (repository *Repository) CredentialReplacementCallback(plan CredentialReplacementPlan, beforePublish func()) (keyring.AuthorityCallback, func() (CredentialReplacementPublication, bool)) {
	var mu sync.Mutex
	var published CredentialReplacementPublication
	var available bool
	result := func() (CredentialReplacementPublication, bool) {
		mu.Lock()
		defer mu.Unlock()
		return published, available
	}
	callback, callbackErr := repository.CredentialAuthorityCallback(plan.Fence)
	if callbackErr != nil || !validID(plan.OperationID) {
		return func(context.Context, *sql.Tx, keyring.AuthorityUpdate) (string, error) { return "", ErrInvalidInput }, result
	}
	return func(ctx context.Context, transaction *sql.Tx, update keyring.AuthorityUpdate) (string, error) {
		registrationRevision, err := validateCredentialReplacementTx(ctx, transaction, plan)
		if err != nil || registrationRevision != plan.Fence.ExpectedRegistrationRevision {
			return "", ErrStaleRevision
		}
		revision, err := callback(ctx, transaction, update)
		if err != nil || update.ValidateOnly {
			return revision, err
		}
		if update.Handle == nil || update.ExactInvalidation {
			return "", ErrInvalidInput
		}
		if beforePublish != nil {
			beforePublish()
		}
		if err := supersedeAuthFlowsTx(ctx, transaction, plan.Fence.ServerID, repository.clock.Now()); err != nil {
			return "", err
		}
		operation, err := insertOperationTx(ctx, transaction, plan.OperationID, plan.Fence.ServerID, contract.OperationCredentialReplace, plan.Fence.ExpectedDesiredRevision, repository.clock.Now())
		if err != nil {
			return "", err
		}
		mu.Lock()
		published = CredentialReplacementPublication{Revision: revision, Operation: operation}
		available = true
		mu.Unlock()
		return revision, nil
	}, result
}

func validateCredentialReplacementTx(ctx context.Context, transaction *sql.Tx, plan CredentialReplacementPlan) (string, error) {
	server, err := serverByIDTx(ctx, transaction, plan.Fence.ServerID)
	if err != nil {
		return "", err
	}
	if server.DesiredState == contract.DesiredServerDeleted {
		return "", ErrInvalidOperation
	}
	if server.DesiredRevision != plan.Fence.ExpectedDesiredRevision {
		return "", ErrStaleRevision
	}
	authority, err := authorityTx(ctx, transaction, server.ID)
	if err != nil {
		return "", err
	}
	currentRevision := authority.CredentialRevisions.StaticCredential
	if plan.Fence.Kind == contract.ServerCredentialOAuthClient {
		currentRevision = authority.CredentialRevisions.OAuthClient
	}
	if currentRevision != plan.Fence.ExpectedCredentialRevision {
		return "", ErrStaleRevision
	}
	var conflicts int
	if err := transaction.QueryRowContext(ctx, `
		SELECT count(*) FROM server_operations
		WHERE server_id = ? AND state IN ('scheduled','running')
		  AND kind IN ('activate','reload','retry','credential_replace','disable','delete','disconnect_credentials')`, server.ID).Scan(&conflicts); err != nil {
		return "", fmt.Errorf("read credential replacement conflicts: %w", err)
	}
	if conflicts != 0 {
		return "", ErrOperationConflict
	}
	if err := validateReplacementMode(server.Transport, plan.Fence.Kind, plan.Slots); err != nil {
		return "", err
	}
	return authority.RegistrationRevision, nil
}

func validateReplacementMode(contents []byte, kind contract.ServerCredentialKind, suppliedSlots []string) error {
	decoded, err := DecodeTransport(contents)
	if err != nil {
		return ErrInvalidInput
	}
	slots := append([]string(nil), suppliedSlots...)
	slices.Sort(slots)
	switch kind {
	case contract.ServerCredentialStatic:
		if len(slots) == 0 || hasDuplicate(slots) {
			return ErrInvalidOperation
		}
		var expected []string
		switch transport := decoded.(type) {
		case contract.StdioTransport:
			seen := make(map[string]struct{}, len(transport.SecretEnvironment))
			for _, slot := range transport.SecretEnvironment {
				seen[slot] = struct{}{}
			}
			for slot := range seen {
				expected = append(expected, slot)
			}
		case contract.StreamableHTTPTransport:
			if _, ok := transport.Authentication.(contract.BearerAuthentication); !ok {
				return ErrInvalidOperation
			}
			expected = []string{"bearer"}
		default:
			return ErrInvalidOperation
		}
		slices.Sort(expected)
		if !slices.Equal(slots, expected) {
			return ErrInvalidOperation
		}
	case contract.ServerCredentialOAuthClient:
		if len(suppliedSlots) != 0 {
			return ErrInvalidOperation
		}
		transport, ok := decoded.(contract.StreamableHTTPTransport)
		if !ok {
			return ErrInvalidOperation
		}
		authentication, ok := transport.Authentication.(contract.OAuthAuthentication)
		if !ok {
			return ErrInvalidOperation
		}
		registration, ok := authentication.Registration.(contract.StaticOAuthRegistration)
		if !ok || (registration.TokenEndpointAuthMethod != contract.TokenEndpointAuthClientSecretBasic && registration.TokenEndpointAuthMethod != contract.TokenEndpointAuthClientSecretPost) {
			return ErrInvalidOperation
		}
	default:
		return ErrInvalidOperation
	}
	return nil
}

func hasDuplicate(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			return true
		}
	}
	return false
}
