// Package servers owns all durable S2 server-domain SQL.
package servers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

var (
	ErrNotFound             = errors.New("server-domain record was not found")
	ErrInvalidInput         = errors.New("server-domain input is invalid")
	ErrIdentityUnavailable  = errors.New("server identity is unavailable")
	ErrNamespaceUnavailable = errors.New("server namespace is unavailable")
	ErrResourceLimit        = errors.New("server-domain resource limit is reached")
	ErrStaleRevision        = errors.New("server desired or authority revision is stale")
	ErrInvalidTransition    = errors.New("server operation transition is invalid")
	ErrIdempotencyConflict  = errors.New("S2 idempotency key conflicts with prior work")
	ErrStaleCursor          = errors.New("server-domain cursor snapshot is stale")
	ErrStorageUnavailable   = errors.New("server-domain storage is unavailable")

	namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

type Repository struct {
	store   *storage.Store
	clock   Clock
	entropy io.Reader
}

func New(store *storage.Store, clock Clock, entropy io.Reader) (*Repository, error) {
	if store == nil || clock == nil || entropy == nil {
		return nil, fmt.Errorf("server repository dependencies are incomplete")
	}
	return &Repository{store: store, clock: clock, entropy: entropy}, nil
}

func (repository *Repository) NewID() (string, error) {
	return admin.NewID(repository.clock.Now(), repository.entropy)
}

func formatTime(value time.Time) string {
	return value.UTC().Format(timestampLayout)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func canonicalJSON(contents []byte) ([]byte, error) {
	var value any
	options := strictjson.Options{
		MaxBytes: mustLimit("api_json_body_bytes"),
		MaxDepth: int(mustLimit("json_depth")),
	}
	if err := strictjson.Decode(contents, &value, options); err != nil {
		return nil, fmt.Errorf("%w: transport JSON: %w", ErrInvalidInput, err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("%w: transport must be a JSON object", ErrInvalidInput)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: transport JSON: %w", ErrInvalidInput, err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize transport JSON: %w", err)
	}
	return canonical, nil
}

func validateDefinition(definition Definition) ([]byte, error) {
	if !namespacePattern.MatchString(definition.Namespace) || definition.Namespace == "mcp_gateway" {
		return nil, fmt.Errorf("%w: namespace", ErrInvalidInput)
	}
	if !utf8.ValidString(definition.DisplayName) || len(definition.DisplayName) < 1 || int64(len(definition.DisplayName)) > mustLimit("display_name_bytes") {
		return nil, fmt.Errorf("%w: display name", ErrInvalidInput)
	}
	return canonicalTransport(definition.Transport)
}

func canonicalTransport(transport contract.Transport) ([]byte, error) {
	if transport == nil {
		return nil, fmt.Errorf("%w: transport", ErrInvalidInput)
	}
	encoded, err := json.Marshal(transport)
	if err != nil {
		return nil, fmt.Errorf("%w: transport: %w", ErrInvalidInput, err)
	}
	return canonicalJSON(encoded)
}

func validID(value string) bool {
	if len(value) != 26 || value[0] < '0' || value[0] > '7' {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", character) {
			return false
		}
	}
	return true
}

func parseRevision(value string) (int64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, ErrStaleRevision
	}
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 0 {
		return 0, ErrStaleRevision
	}
	return revision, nil
}

func mustLimit(name string) int64 {
	limit, ok := contract.FixedLimitByName(name)
	if !ok {
		panic("missing server-domain limit: " + name)
	}
	return limit.Maximum
}

func mapMutationError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrStorageLatched) {
		return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}
	if errors.Is(err, storage.ErrMutationBusy) {
		return fmt.Errorf("%w: %w", ErrResourceLimit, err)
	}
	return err
}

func mapViewError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func validatePageLimit(limit int) error {
	returnedLimit := int64(limit)
	if returnedLimit < 1 || returnedLimit > mustLimit("s2_list_page") {
		return ErrInvalidInput
	}
	return nil
}

func ensureServerExists(ctx context.Context, transaction *sql.Tx, serverID string) error {
	var exists int
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM servers WHERE id = ?`, serverID).Scan(&exists); err != nil {
		return fmt.Errorf("inspect server identity: %w", err)
	}
	if exists != 1 {
		return ErrNotFound
	}
	return nil
}
