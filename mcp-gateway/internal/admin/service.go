package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

const (
	bearerEntropyBytes = 32
	ulidEntropyBytes   = 10
)

var (
	ErrAlreadyInitialized       = errors.New("admin authority is already initialized")
	ErrNotInitialized           = errors.New("admin authority is not initialized")
	ErrAuthenticationRequired   = errors.New("authentication is required")
	ErrCredentialDomainMismatch = errors.New("credential is for a different authority")
	ErrSecretPublication        = errors.New("secret publication failed")
)

type Clock interface {
	Now() time.Time
}

type SecretSink interface {
	Publish(string) error
}

type Service struct {
	store     *storage.Store
	clock     Clock
	entropy   io.Reader
	secretOps sync.Mutex

	invalidationMu   sync.Mutex
	nextInvalidation uint64
	invalidations    map[uint64]func(*string)
}

type credentialCandidate struct {
	metadata contract.AdminCredential
	verifier [sha256.Size]byte
	bearer   string
}

func NewService(store *storage.Store, clock Clock, entropy io.Reader) *Service {
	return &Service{
		store:         store,
		clock:         clock,
		entropy:       entropy,
		invalidations: make(map[uint64]func(*string)),
	}
}

func (service *Service) Initialize(ctx context.Context, sink SecretSink) (contract.AdminCredential, error) {
	service.secretOps.Lock()
	defer service.secretOps.Unlock()

	initialized, err := service.hasCredentials(ctx)
	if err != nil {
		return contract.AdminCredential{}, err
	}
	if initialized {
		return contract.AdminCredential{}, ErrAlreadyInitialized
	}
	candidate, err := service.prepareCredential(nil)
	if err != nil {
		return contract.AdminCredential{}, err
	}
	if err := sink.Publish(candidate.bearer); err != nil {
		return contract.AdminCredential{}, fmt.Errorf("%w: %w", ErrSecretPublication, err)
	}
	if err := service.store.Mutate(ctx, func(transaction *sql.Tx) error {
		var count int
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM admin_credentials`).Scan(&count); err != nil {
			return fmt.Errorf("count admin credentials: %w", err)
		}
		if count != 0 {
			return ErrAlreadyInitialized
		}
		revision, err := incrementRevision(ctx, transaction)
		if err != nil {
			return err
		}
		candidate.metadata.Revision = revision
		return insertCredential(ctx, transaction, candidate)
	}); err != nil {
		return contract.AdminCredential{}, err
	}
	return candidate.metadata, nil
}

func (service *Service) Reset(ctx context.Context, sink SecretSink) (contract.AdminCredential, error) {
	service.secretOps.Lock()
	defer service.secretOps.Unlock()

	initialized, err := service.hasCredentials(ctx)
	if err != nil {
		return contract.AdminCredential{}, err
	}
	if !initialized {
		return contract.AdminCredential{}, ErrNotInitialized
	}
	candidate, err := service.prepareCredential(nil)
	if err != nil {
		return contract.AdminCredential{}, err
	}
	if err := sink.Publish(candidate.bearer); err != nil {
		return contract.AdminCredential{}, fmt.Errorf("%w: %w", ErrSecretPublication, err)
	}
	if err := service.store.Mutate(ctx, func(transaction *sql.Tx) error {
		revision, err := incrementRevision(ctx, transaction)
		if err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE admin_credentials SET status = 'revoked', revision = ?
			WHERE status = 'active'`, revision); err != nil {
			return fmt.Errorf("revoke prior admin credentials: %w", err)
		}
		if err := pruneForInsert(ctx, transaction, service.clock.Now()); err != nil {
			return err
		}
		candidate.metadata.Revision = revision
		return insertCredential(ctx, transaction, candidate)
	}); err != nil {
		return contract.AdminCredential{}, err
	}
	service.notifyCredentialInvalidation(nil)
	return candidate.metadata, nil
}

func (service *Service) Authenticate(ctx context.Context, bearer string) (contract.AdminCredential, error) {
	if strings.HasPrefix(bearer, contract.AgentBearerPrefix) {
		return contract.AdminCredential{}, ErrCredentialDomainMismatch
	}
	if !strings.HasPrefix(bearer, contract.AdminBearerPrefix) {
		return contract.AdminCredential{}, ErrAuthenticationRequired
	}
	verifier := verifierForBearer(bearer)
	var matched contract.AdminCredential
	found := false
	err := service.store.View(ctx, func(transaction *sql.Tx) error {
		rows, err := transaction.QueryContext(ctx, `
			SELECT id, verifier, fingerprint, created_at, expires_at, status, revision
			FROM admin_credentials WHERE status = 'active'`)
		if err != nil {
			return fmt.Errorf("read active admin credentials: %w", err)
		}
		defer func() { _ = rows.Close() }()
		now := service.clock.Now()
		for rows.Next() {
			record, err := scanCredential(rows, now)
			if err != nil {
				return fmt.Errorf("scan active admin credential: %w", err)
			}
			matches := subtle.ConstantTimeCompare(record.verifier, verifier[:]) == 1
			if matches && record.metadata.Status == contract.CredentialActive {
				matched = record.metadata
				found = true
			}
		}
		return rows.Err()
	})
	if err != nil {
		return contract.AdminCredential{}, err
	}
	if !found {
		return contract.AdminCredential{}, ErrAuthenticationRequired
	}
	return matched, nil
}

func (service *Service) hasCredentials(ctx context.Context) (bool, error) {
	var count int
	err := service.store.View(ctx, func(transaction *sql.Tx) error {
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM admin_credentials`).Scan(&count); err != nil {
			return fmt.Errorf("count admin credentials: %w", err)
		}
		return nil
	})
	return count != 0, err
}

func (service *Service) prepareCredential(expiresAt *time.Time) (credentialCandidate, error) {
	secret := make([]byte, bearerEntropyBytes)
	if _, err := io.ReadFull(service.entropy, secret); err != nil {
		return credentialCandidate{}, fmt.Errorf("generate admin bearer: %w", err)
	}
	id, err := NewID(service.clock.Now(), service.entropy)
	if err != nil {
		return credentialCandidate{}, err
	}
	bearer := contract.AdminBearerPrefix + base64.RawURLEncoding.EncodeToString(secret)
	verifier := verifierForBearer(bearer)
	fingerprintDigest := sha256.Sum256(append([]byte("mcp-gateway/admin-fingerprint/v1\x00"), verifier[:]...))
	createdAt := service.clock.Now().UTC().Format(time.RFC3339Nano)
	metadata := contract.AdminCredential{
		ID:          id,
		Fingerprint: hex.EncodeToString(fingerprintDigest[:8]),
		CreatedAt:   createdAt,
		NonExpiring: expiresAt == nil,
		Status:      contract.CredentialActive,
	}
	if expiresAt != nil {
		value := expiresAt.UTC().Format(time.RFC3339Nano)
		metadata.ExpiresAt = &value
	}
	return credentialCandidate{metadata: metadata, verifier: verifier, bearer: bearer}, nil
}

func (service *Service) SubscribeCredentialInvalidations(callback func(*string)) func() {
	service.invalidationMu.Lock()
	id := service.nextInvalidation
	service.nextInvalidation++
	service.invalidations[id] = callback
	service.invalidationMu.Unlock()
	return func() {
		service.invalidationMu.Lock()
		delete(service.invalidations, id)
		service.invalidationMu.Unlock()
	}
}

func (service *Service) notifyCredentialInvalidation(id *string) {
	service.invalidationMu.Lock()
	callbacks := make([]func(*string), 0, len(service.invalidations))
	for _, callback := range service.invalidations {
		callbacks = append(callbacks, callback)
	}
	service.invalidationMu.Unlock()
	for _, callback := range callbacks {
		callback(id)
	}
}

func verifierForBearer(bearer string) [sha256.Size]byte {
	return sha256.Sum256(append([]byte("mcp-gateway/admin-verifier/v1\x00"), bearer...))
}

func incrementRevision(ctx context.Context, transaction *sql.Tx) (string, error) {
	var revision uint64
	if err := transaction.QueryRowContext(ctx, `
		UPDATE gateway_meta SET revision = revision + 1 WHERE singleton = 1
		RETURNING revision`).Scan(&revision); err != nil {
		return "", fmt.Errorf("increment Gateway revision: %w", err)
	}
	return fmt.Sprintf("%d", revision), nil
}

func insertCredential(ctx context.Context, transaction *sql.Tx, candidate credentialCandidate) error {
	_, err := transaction.ExecContext(ctx, `
		INSERT INTO admin_credentials
			(id, verifier, fingerprint, created_at, expires_at, status, revision)
		VALUES (?, ?, ?, ?, ?, 'active', ?)`,
		candidate.metadata.ID,
		candidate.verifier[:],
		candidate.metadata.Fingerprint,
		candidate.metadata.CreatedAt,
		candidate.metadata.ExpiresAt,
		candidate.metadata.Revision,
	)
	if err != nil {
		return fmt.Errorf("insert admin credential: %w", err)
	}
	return nil
}
