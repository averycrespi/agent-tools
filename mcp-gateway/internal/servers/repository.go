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
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
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
	ErrInvalidOperation     = errors.New("server operation is not admissible")
	ErrOperationConflict    = errors.New("server has conflicting work")
	ErrIdempotencyConflict  = errors.New("S2 idempotency key conflicts with prior work")
	ErrStaleCursor          = errors.New("server-domain cursor snapshot is stale")
	ErrStorageUnavailable   = errors.New("server-domain storage is unavailable")

	namespacePattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	secretSlotPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
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

func (repository *Repository) RegistryStatus(ctx context.Context) (contract.LimitStatus, contract.LimitStatus, error) {
	var identities, activeServers int64
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM server_identities`).Scan(&identities); err != nil {
			return fmt.Errorf("count server identities: %w", err)
		}
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM servers WHERE desired_state != 'deleted'`).Scan(&activeServers); err != nil {
			return fmt.Errorf("count servers: %w", err)
		}
		return nil
	})
	identityLimit := mustLimit("server_identities")
	serverLimit := mustLimit("servers")
	return contract.LimitStatus{InUse: identities, Limit: identityLimit, Saturated: identities >= identityLimit}, contract.LimitStatus{InUse: activeServers, Limit: serverLimit, Saturated: activeServers >= serverLimit}, mapViewError(err)
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
	var err error
	switch value := transport.(type) {
	case contract.StdioTransport:
		err = validateStdioTransport(value)
	case contract.StreamableHTTPTransport:
		err = validateHTTPTransport(value)
	default:
		err = fmt.Errorf("unsupported transport type %T", transport)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: transport: %w", ErrInvalidInput, err)
	}
	encoded, err := json.Marshal(transport)
	if err != nil {
		return nil, fmt.Errorf("%w: transport: %w", ErrInvalidInput, err)
	}
	return canonicalJSON(encoded)
}

func validateStdioTransport(transport contract.StdioTransport) error {
	if transport.Kind != contract.TransportStdio || !validAbsolutePath(transport.Executable) || !validAbsolutePath(transport.WorkingDirectory) {
		return errors.New("stdio path or kind is invalid")
	}
	if len(transport.Arguments) > int(mustLimit("stdio_arguments")) || len(transport.Environment) > int(mustLimit("stdio_environment_entries")) || len(transport.SecretEnvironment) > int(mustLimit("stdio_secret_environment_entries")) {
		return errors.New("stdio collection limit exceeded")
	}
	totalArguments := 0
	for _, argument := range transport.Arguments {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) || int64(len(argument)) > mustLimit("stdio_argument_bytes") {
			return errors.New("stdio argument is invalid")
		}
		totalArguments += len(argument)
	}
	if int64(totalArguments) > mustLimit("stdio_arguments_bytes") {
		return errors.New("stdio argument bytes exceeded")
	}
	for name, value := range transport.Environment {
		if !validEnvironmentName(name) || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || int64(len(value)) > mustLimit("stdio_environment_value_bytes") {
			return errors.New("stdio environment is invalid")
		}
		if _, secret := transport.SecretEnvironment[name]; secret {
			return errors.New("stdio environment source is ambiguous")
		}
	}
	for name, slot := range transport.SecretEnvironment {
		if !validEnvironmentName(name) || !secretSlotPattern.MatchString(slot) {
			return errors.New("stdio secret environment is invalid")
		}
	}
	return nil
}

func validAbsolutePath(value string) bool {
	return utf8.ValidString(value) && value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, 0) && int64(len(value)) <= mustLimit("stdio_path_bytes")
}

func validEnvironmentName(value string) bool {
	return utf8.ValidString(value) && value != "" && !strings.ContainsAny(value, "=\x00") && int64(len(value)) <= mustLimit("stdio_environment_name_bytes")
}

func validateHTTPTransport(transport contract.StreamableHTTPTransport) error {
	if transport.Kind != contract.TransportStreamableHTTP {
		return errors.New("HTTP transport kind is invalid")
	}
	if _, err := contract.ParseProtocolMode(string(transport.ProtocolMode)); err != nil {
		return err
	}
	endpoint, err := parseEndpointURL(transport.URL)
	if err != nil {
		return err
	}
	mode := authenticationMode(transport.Authentication)
	if _, err := contract.ParseAuthenticationMode(string(mode)); err != nil {
		return err
	}
	if endpoint.Scheme == "http" && (mode != contract.AuthenticationNone || !isLoopbackLiteral(endpoint.Hostname())) {
		return errors.New("plain HTTP requires unauthenticated loopback IP")
	}
	if mode == contract.AuthenticationOAuth && endpoint.Scheme != "https" {
		return errors.New("OAuth requires HTTPS")
	}
	return validateHTTPAuthentication(transport.Authentication)
}

func authenticationMode(authentication contract.HTTPAuthentication) contract.AuthenticationMode {
	switch value := authentication.(type) {
	case contract.NoAuthentication:
		return value.Mode
	case contract.BearerAuthentication:
		return value.Mode
	case contract.OAuthAuthentication:
		return value.Mode
	default:
		return ""
	}
}

func validateHTTPAuthentication(authentication contract.HTTPAuthentication) error {
	switch value := authentication.(type) {
	case contract.NoAuthentication:
		if value.Mode != contract.AuthenticationNone {
			return errors.New("authentication union mismatch")
		}
	case contract.BearerAuthentication:
		if value.Mode != contract.AuthenticationBearer {
			return errors.New("authentication union mismatch")
		}
	case contract.OAuthAuthentication:
		if value.Mode != contract.AuthenticationOAuth {
			return errors.New("authentication union mismatch")
		}
		if len(value.TrustedOrigins) > 64 {
			return errors.New("too many trusted origins")
		}
		seen := make(map[string]struct{}, len(value.TrustedOrigins))
		for _, origin := range value.TrustedOrigins {
			if _, err := remote.ParseOrigin(origin); err != nil {
				return errors.New("trusted origin is invalid")
			}
			if _, duplicate := seen[origin]; duplicate {
				return errors.New("trusted origin is duplicated")
			}
			seen[origin] = struct{}{}
		}
		return validateRegistration(value.Registration)
	default:
		return errors.New("authentication union is invalid")
	}
	return nil
}

func validateRegistration(registration contract.OAuthRegistration) error {
	var issuer *string
	switch value := registration.(type) {
	case contract.StaticOAuthRegistration:
		if value.Mode != contract.RegistrationStatic || value.ClientID == "" || !utf8.ValidString(value.ClientID) || int64(len(value.ClientID)) > mustLimit("oauth_client_id_bytes") {
			return errors.New("static registration is invalid")
		}
		if _, err := contract.ParseTokenEndpointAuthMethod(string(value.TokenEndpointAuthMethod)); err != nil {
			return err
		}
		issuer = value.Issuer
	case contract.DynamicOAuthRegistration:
		if value.Mode != contract.RegistrationDynamic {
			return errors.New("dynamic registration is invalid")
		}
		issuer = value.Issuer
	default:
		return errors.New("registration union is invalid")
	}
	if issuer != nil {
		parsed, err := url.Parse(*issuer)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != *issuer || strings.ToLower(parsed.Hostname()) != parsed.Hostname() || parsed.Port() == "443" || int64(len(*issuer)) > mustLimit("oauth_url_bytes") {
			return errors.New("issuer is invalid")
		}
	}
	return nil
}

func parseEndpointURL(value string) (*url.URL, error) {
	if !utf8.ValidString(value) || value == "" || int64(len(value)) > mustLimit("resource_url_bytes") {
		return nil, errors.New("resource URL is invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != value || strings.ToLower(parsed.Hostname()) != parsed.Hostname() || parsed.Scheme == "https" && parsed.Port() == "443" || parsed.Scheme == "http" && parsed.Port() == "80" {
		return nil, errors.New("resource URL is invalid")
	}
	return parsed, nil
}

func isLoopbackLiteral(host string) bool {
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
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
