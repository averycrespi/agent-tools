package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"

	"github.com/averycrespi/agent-tools/http-broker/internal/atomicfile"
	brokerpaths "github.com/averycrespi/agent-tools/http-broker/internal/paths"
)

// Role identifies one strictly separated broker credential.
type Role string

const (
	AgentRole Role = "agent"
	AdminRole Role = "admin"
)

// TokenSet is an immutable pair of role credentials.
type TokenSet struct {
	Agent string
	Admin string
}

// TokenPaths contains the canonical, migration-only, and lock paths.
type TokenPaths struct {
	Agent  string
	Admin  string
	Legacy string
	Lock   string
}

// DefaultTokenPaths returns the token paths under the XDG config directory.
func DefaultTokenPaths() TokenPaths {
	return TokenPaths{
		Agent:  brokerpaths.AgentTokenFile(),
		Admin:  brokerpaths.AdminTokenFile(),
		Legacy: brokerpaths.LegacyTokenFile(),
		Lock:   brokerpaths.TokenLockFile(),
	}
}

// EnsureTokenSet initializes or migrates the canonical role credentials.
func EnsureTokenSet(paths TokenPaths) (TokenSet, error) {
	return EnsureTokenSetContext(context.Background(), paths)
}

// EnsureTokenSetContext initializes or migrates credentials with bounded lock acquisition.
func EnsureTokenSetContext(ctx context.Context, paths TokenPaths) (tokens TokenSet, err error) {
	unlock, err := lockTokenPaths(ctx, paths)
	if err != nil {
		return TokenSet{}, err
	}
	defer func() {
		if unlockErr := unlock(); err == nil && unlockErr != nil {
			err = unlockErr
		}
	}()

	return ensureTokenSetLocked(paths)
}

// RotateToken atomically replaces only the selected canonical credential.
func RotateToken(paths TokenPaths, role Role) (TokenSet, error) {
	return RotateTokenContext(context.Background(), paths, role)
}

// RotateTokenContext rotates one role with bounded lock acquisition.
func RotateTokenContext(ctx context.Context, paths TokenPaths, role Role) (tokens TokenSet, err error) {
	if role != AgentRole && role != AdminRole {
		return TokenSet{}, fmt.Errorf("invalid token role %q", role)
	}

	unlock, err := lockTokenPaths(ctx, paths)
	if err != nil {
		return TokenSet{}, err
	}
	defer func() {
		if unlockErr := unlock(); err == nil && unlockErr != nil {
			err = unlockErr
		}
	}()

	agent, agentExists, agentErr := loadOptionalToken(paths.Agent)
	admin, adminExists, adminErr := loadOptionalToken(paths.Admin)
	if agentErr != nil {
		return TokenSet{}, agentErr
	}
	if adminErr != nil {
		return TokenSet{}, adminErr
	}

	if !agentExists || !adminExists {
		tokens, err = ensureTokenSetLocked(paths)
		if err != nil {
			return TokenSet{}, err
		}
	} else {
		tokens = TokenSet{Agent: agent, Admin: admin}
	}
	if err := retireLegacy(paths); err != nil {
		return TokenSet{}, err
	}

	switch role {
	case AgentRole:
		tokens.Agent, err = generateDistinctToken(tokens.Admin)
		if err == nil {
			err = writeToken(paths.Agent, tokens.Agent)
		}
	case AdminRole:
		tokens.Admin, err = generateDistinctToken(tokens.Agent)
		if err == nil {
			err = writeToken(paths.Admin, tokens.Admin)
		}
	}
	if err != nil {
		return TokenSet{}, err
	}
	return tokens, nil
}

func ensureTokenSetLocked(paths TokenPaths) (TokenSet, error) {
	agent, agentExists, err := loadOptionalToken(paths.Agent)
	if err != nil {
		return TokenSet{}, err
	}
	admin, adminExists, err := loadOptionalToken(paths.Admin)
	if err != nil {
		return TokenSet{}, err
	}

	if agentExists && adminExists {
		tokens := TokenSet{Agent: agent, Admin: admin}
		if err := validateTokenSet(tokens); err != nil {
			return TokenSet{}, err
		}
		if err := retireLegacy(paths); err != nil {
			return TokenSet{}, err
		}
		return tokens, nil
	}

	if !agentExists {
		legacy, legacyExists, legacyErr := loadOptionalToken(paths.Legacy)
		if legacyErr != nil {
			return TokenSet{}, fmt.Errorf("loading migration token %s: %w", paths.Legacy, legacyErr)
		}
		if legacyExists {
			agent = legacy
		} else {
			agent, err = generateDistinctToken(admin)
			if err != nil {
				return TokenSet{}, err
			}
		}
		if err := writeToken(paths.Agent, agent); err != nil {
			return TokenSet{}, err
		}
	}

	if !adminExists {
		admin, err = generateDistinctToken(agent)
		if err != nil {
			return TokenSet{}, err
		}
		if err := writeToken(paths.Admin, admin); err != nil {
			return TokenSet{}, err
		}
	}

	tokens := TokenSet{Agent: agent, Admin: admin}
	if err := validateTokenSet(tokens); err != nil {
		return TokenSet{}, err
	}
	if err := retireLegacy(paths); err != nil {
		return TokenSet{}, err
	}
	return tokens, nil
}

const tokenLockTimeout = 10 * time.Second

func lockTokenPaths(ctx context.Context, paths TokenPaths) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(paths.Lock), 0o750); err != nil {
		return nil, fmt.Errorf("creating token directory: %w", err)
	}
	fileLock := flock.New(paths.Lock)
	lockCtx, cancel := context.WithTimeout(ctx, tokenLockTimeout)
	defer cancel()
	locked, err := fileLock.TryLockContext(lockCtx, 10*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("locking token files: %w", err)
	}
	if !locked {
		return nil, errors.New("locking token files: lock not acquired")
	}
	return func() error {
		if err := fileLock.Unlock(); err != nil {
			return fmt.Errorf("unlocking token files: %w", err)
		}
		return nil
	}, nil
}

func loadOptionalToken(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading token file %s: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if err := validateToken(token); err != nil {
		return "", true, fmt.Errorf("validating token file %s: %w", path, err)
	}
	return token, true, nil
}

func validateToken(token string) error {
	if len(token) != 64 {
		return errors.New("token must contain exactly 64 lowercase hexadecimal characters")
	}
	for _, char := range token {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return errors.New("token must contain exactly 64 lowercase hexadecimal characters")
		}
	}
	return nil
}

func validateTokenSet(tokens TokenSet) error {
	if err := validateToken(tokens.Agent); err != nil {
		return fmt.Errorf("invalid agent token: %w", err)
	}
	if err := validateToken(tokens.Admin); err != nil {
		return fmt.Errorf("invalid admin token: %w", err)
	}
	if tokens.Agent == tokens.Admin {
		return errors.New("agent and admin tokens must be distinct")
	}
	return nil
}

var tokenRandom io.Reader = rand.Reader

func generateDistinctToken(opposite string) (string, error) {
	for range 16 {
		bytes := make([]byte, 32)
		if _, err := io.ReadFull(tokenRandom, bytes); err != nil {
			return "", fmt.Errorf("generating token: %w", err)
		}
		token := hex.EncodeToString(bytes)
		if token != opposite {
			return token, nil
		}
	}
	return "", errors.New("generating distinct token: too many collisions")
}

func writeToken(path, token string) error {
	if err := atomicfile.Write(path, []byte(token), 0o600); err != nil {
		return fmt.Errorf("writing token file %s: %w", path, err)
	}
	return nil
}

func retireLegacy(paths TokenPaths) error {
	err := os.Remove(paths.Legacy)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("retiring migration token %s: %w", paths.Legacy, err)
	}
	if err := atomicfile.SyncDirectory(filepath.Dir(paths.Legacy)); err != nil {
		return fmt.Errorf("syncing token directory after migration: %w", err)
	}
	return nil
}

// Store atomically publishes immutable token-pair snapshots.
type Store struct {
	current atomic.Pointer[TokenSet]
}

// NewStore creates a live token store from a valid distinct pair.
func NewStore(tokens TokenSet) (*Store, error) {
	if err := validateTokenSet(tokens); err != nil {
		return nil, err
	}
	store := &Store{}
	store.current.Store(&tokens)
	return store, nil
}

// Snapshot returns one complete immutable token pair.
func (s *Store) Snapshot() TokenSet {
	return *s.current.Load()
}

// ReloadResult reports the safely published pair and any rejected role candidates.
type ReloadResult struct {
	Tokens   TokenSet
	AgentErr error
	AdminErr error
}

// Reload independently validates both disk candidates and atomically publishes one safe pair.
func (s *Store) Reload(paths TokenPaths) ReloadResult {
	previous := s.Snapshot()
	result := ReloadResult{Tokens: previous}

	agent, exists, err := loadOptionalToken(paths.Agent)
	switch {
	case err != nil:
		result.AgentErr = err
	case !exists:
		result.AgentErr = fmt.Errorf("agent token file %s is missing", paths.Agent)
	case agent == previous.Admin && agent != previous.Agent:
		result.AgentErr = errors.New("agent token candidate matches the prior admin token")
	default:
		result.Tokens.Agent = agent
	}

	admin, exists, err := loadOptionalToken(paths.Admin)
	switch {
	case err != nil:
		result.AdminErr = err
	case !exists:
		result.AdminErr = fmt.Errorf("admin token file %s is missing", paths.Admin)
	case admin == previous.Agent && admin != previous.Admin:
		result.AdminErr = errors.New("admin token candidate matches the prior agent token")
	default:
		result.Tokens.Admin = admin
	}

	if result.Tokens.Agent == result.Tokens.Admin {
		agentChanged := result.Tokens.Agent != previous.Agent
		adminChanged := result.Tokens.Admin != previous.Admin
		if agentChanged {
			result.Tokens.Agent = previous.Agent
			result.AgentErr = errors.New("agent token candidate would merge role credentials")
		}
		if adminChanged {
			result.Tokens.Admin = previous.Admin
			result.AdminErr = errors.New("admin token candidate would merge role credentials")
		}
	}

	next := result.Tokens
	s.current.Store(&next)
	return result
}
