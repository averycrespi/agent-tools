package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

const (
	ApplicationID           = 0x4d475731
	CurrentSchema           = 10
	BusyTimeoutMilliseconds = 2000
	connectionLimit         = 4
)

var (
	ErrInvalidDatabase       = errors.New("invalid Gateway database")
	ErrNewerSchema           = errors.New("gateway database schema is newer than this binary")
	ErrAlreadyInitialized    = errors.New("gateway is already initialized")
	ErrInvalidInstallationID = errors.New("invalid installation ID")
	installationIDPattern    = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrationNames = [...]string{"001_initial.sql", "002_admin_credentials.sql", "003_keyring_generations.sql", "004_servers.sql", "005_auth_flows.sql", "006_catalogs.sql", "007_retired_catalogs.sql", "008_authorization.sql", "009_invocations.sql", "010_grant_requests.sql"}

type Identity struct {
	InstallationID string
	SchemaVersion  int
	Revision       uint64
}

type Settings struct {
	ApplicationID           int
	JournalMode             string
	Synchronous             int
	ForeignKeys             bool
	BusyTimeoutMilliseconds int
	Integrity               string
}

type Store struct {
	database      *sql.DB
	path          string
	marker        mutationMarker
	mutationSlot  chan struct{}
	fault         func(FaultPoint) error
	databaseLimit int64
	latched       atomic.Bool
}

type testOptions struct {
	fault             func(FaultPoint) error
	databaseByteLimit int64
}

func Initialize(ctx context.Context, ownership *gatewaypaths.Ownership, installationID string) (*Store, error) {
	return initializeWithOptions(ctx, ownership, installationID, testOptions{})
}

// InitializeWithFaultInjection is an internal test seam for deterministic marker and commit faults.
func InitializeWithFaultInjection(
	ctx context.Context,
	ownership *gatewaypaths.Ownership,
	installationID string,
	fault func(FaultPoint) error,
) (*Store, error) {
	return initializeWithOptions(ctx, ownership, installationID, testOptions{fault: fault})
}

func initializeWithOptions(ctx context.Context, ownership *gatewaypaths.Ownership, installationID string, options testOptions) (*Store, error) {
	if !installationIDPattern.MatchString(installationID) {
		return nil, ErrInvalidInstallationID
	}
	layout, err := ownership.ActiveLayout()
	if err != nil {
		return nil, fmt.Errorf("require active installation ownership: %w", err)
	}
	marker := newMutationMarker(layout, options.fault)
	marked, err := marker.hasArtifacts()
	if err != nil || marked {
		return nil, fmt.Errorf("%w: mutation marker state prevents initialization", ErrStorageLatched)
	}
	file, err := gatewaypaths.CreateOwnerOnlyFile(layout.Database)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrAlreadyInitialized
		}
		return nil, fmt.Errorf("create Gateway database: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(layout.Database)
		return nil, fmt.Errorf("close new Gateway database: %w", err)
	}

	store, err := openConfigured(ctx, layout, options)
	if err != nil {
		removeDatabaseGeneration(layout.Database)
		return nil, err
	}
	initialized := false
	defer func() {
		if !initialized {
			_ = store.Close()
			removeDatabaseGeneration(layout.Database)
		}
	}()
	if err := store.bootstrap(ctx, installationID); err != nil {
		return nil, err
	}
	if err := store.configureSizeLimit(ctx); err != nil {
		return nil, err
	}
	if err := store.migrate(ctx, 0); err != nil {
		return nil, err
	}
	if err := store.verify(ctx); err != nil {
		return nil, err
	}
	initialized = true
	return store, nil
}

func Open(ctx context.Context, ownership *gatewaypaths.Ownership) (*Store, error) {
	return openWithOptions(ctx, ownership, testOptions{})
}

func openWithOptions(ctx context.Context, ownership *gatewaypaths.Ownership, options testOptions) (*Store, error) {
	layout, err := ownership.ActiveLayout()
	if err != nil {
		return nil, fmt.Errorf("require active installation ownership: %w", err)
	}
	if err := gatewaypaths.ValidateOwnerOnlyFile(layout.Database); err != nil {
		return nil, fmt.Errorf("%w: database path: %w", ErrInvalidDatabase, err)
	}
	version, err := inspectDatabase(ctx, layout.Database)
	if err != nil {
		return nil, err
	}
	if version > CurrentSchema {
		return nil, fmt.Errorf("%w: found %d, support through %d", ErrNewerSchema, version, CurrentSchema)
	}
	marker := newMutationMarker(layout, options.fault)
	marked, err := marker.hasArtifacts()
	if err != nil {
		return nil, fmt.Errorf("%w: inspect mutation marker: %w", ErrStorageLatched, err)
	}
	if marked && version != CurrentSchema {
		return nil, fmt.Errorf("%w: an armed prior-schema database cannot migrate online", ErrStorageLatched)
	}
	store, err := openConfigured(ctx, layout, options)
	if err != nil {
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			_ = store.Close()
		}
	}()
	if !marked {
		if err := store.configureSizeLimit(ctx); err != nil {
			return nil, err
		}
		if err := store.migrate(ctx, version); err != nil {
			return nil, err
		}
	}
	if err := store.verify(ctx); err != nil {
		return nil, err
	}
	overLimit, err := store.overDatabaseLimit(ctx)
	if err != nil {
		return nil, err
	}
	store.latched.Store(marked || overLimit)
	opened = true
	return store, nil
}

func (store *Store) Close() error {
	if err := store.database.Close(); err != nil {
		return fmt.Errorf("close Gateway database: %w", err)
	}
	return nil
}

func (store *Store) Identity(ctx context.Context) (Identity, error) {
	var identity Identity
	var revision int64
	if err := store.database.QueryRowContext(ctx, `
		SELECT installation_id, revision, (SELECT user_version FROM pragma_user_version)
		FROM gateway_meta WHERE singleton = 1`).Scan(
		&identity.InstallationID,
		&revision,
		&identity.SchemaVersion,
	); err != nil {
		return Identity{}, fmt.Errorf("read Gateway identity: %w", err)
	}
	if !installationIDPattern.MatchString(identity.InstallationID) || revision < 0 {
		return Identity{}, fmt.Errorf("%w: malformed installation identity", ErrInvalidDatabase)
	}
	identity.Revision = uint64(revision)
	return identity, nil
}

func (store *Store) View(ctx context.Context, view func(*sql.Tx) error) error {
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin storage view: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := view(transaction); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit storage view: %w", err)
	}
	return nil
}

func (store *Store) Settings(ctx context.Context) (Settings, error) {
	var settings Settings
	if err := store.database.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&settings.ApplicationID); err != nil {
		return Settings{}, fmt.Errorf("read SQLite application ID: %w", err)
	}
	if err := store.database.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&settings.JournalMode); err != nil {
		return Settings{}, fmt.Errorf("read SQLite journal mode: %w", err)
	}
	if err := store.database.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&settings.Synchronous); err != nil {
		return Settings{}, fmt.Errorf("read SQLite synchronous mode: %w", err)
	}
	if err := store.database.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&settings.ForeignKeys); err != nil {
		return Settings{}, fmt.Errorf("read SQLite foreign-key setting: %w", err)
	}
	if err := store.database.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&settings.BusyTimeoutMilliseconds); err != nil {
		return Settings{}, fmt.Errorf("read SQLite busy timeout: %w", err)
	}
	if err := store.database.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&settings.Integrity); err != nil {
		return Settings{}, fmt.Errorf("run SQLite integrity check: %w", err)
	}
	return settings, nil
}

func (store *Store) MigrationVersions(ctx context.Context) ([]int, error) {
	rows, err := store.database.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read migration history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	versions := make([]int, 0, CurrentSchema)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("read migration version: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration history: %w", err)
	}
	return versions, nil
}

func (store *Store) bootstrap(ctx context.Context, installationID string) error {
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin Gateway bootstrap: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, `PRAGMA application_id = `+strconv.Itoa(ApplicationID)); err != nil {
		return fmt.Errorf("set Gateway application ID: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		CREATE TABLE gateway_bootstrap (
			installation_id TEXT NOT NULL,
			revision INTEGER NOT NULL CHECK (revision >= 0)
		) STRICT`); err != nil {
		return fmt.Errorf("create Gateway bootstrap identity: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO gateway_bootstrap VALUES (?, 0)`, installationID); err != nil {
		return fmt.Errorf("write Gateway bootstrap identity: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit Gateway bootstrap: %w", err)
	}
	return nil
}

func (store *Store) migrate(ctx context.Context, version int) error {
	return store.migrateThrough(ctx, version, CurrentSchema)
}

func (store *Store) migrateThrough(ctx context.Context, version, target int) error {
	if len(migrationNames) != CurrentSchema {
		return fmt.Errorf("compiled migration list has %d entries, want %d", len(migrationNames), CurrentSchema)
	}
	if target < version || target > CurrentSchema {
		return fmt.Errorf("invalid migration target %d from version %d", target, version)
	}
	for next := version + 1; next <= target; next++ {
		contents, err := migrationFiles.ReadFile("migrations/" + migrationNames[next-1])
		if err != nil {
			return fmt.Errorf("read migration %d: %w", next, err)
		}
		transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", next, err)
		}
		if _, err := transaction.ExecContext(ctx, string(contents)); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("%w: apply migration %d: %w", ErrInvalidDatabase, next, err)
		}
		if _, err := transaction.ExecContext(ctx, `PRAGMA user_version = `+strconv.Itoa(next)); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("set schema version %d: %w", next, err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", next, err)
		}
	}
	return nil
}

func (store *Store) verify(ctx context.Context) error {
	identity, err := store.Identity(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDatabase, err)
	}
	if identity.SchemaVersion != CurrentSchema {
		return fmt.Errorf("%w: schema version is %d", ErrInvalidDatabase, identity.SchemaVersion)
	}
	settings, err := store.Settings(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDatabase, err)
	}
	if settings.ApplicationID != ApplicationID || settings.JournalMode != "wal" || settings.Synchronous != 2 ||
		!settings.ForeignKeys || settings.BusyTimeoutMilliseconds != BusyTimeoutMilliseconds || settings.Integrity != "ok" {
		return fmt.Errorf("%w: SQLite settings do not match the durability contract", ErrInvalidDatabase)
	}
	if err := store.verifySizeLimit(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDatabase, err)
	}
	versions, err := store.MigrationVersions(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDatabase, err)
	}
	if len(versions) != CurrentSchema {
		return fmt.Errorf("%w: migration history has %d entries", ErrInvalidDatabase, len(versions))
	}
	for index, version := range versions {
		if version != index+1 {
			return fmt.Errorf("%w: migration history is not ordered", ErrInvalidDatabase)
		}
	}
	if err := store.verifyInvocationStructure(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDatabase, err)
	}
	if err := store.verifyGrantRequestStructure(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDatabase, err)
	}
	return nil
}

func (store *Store) verifyInvocationStructure(ctx context.Context) error {
	expectedColumns := []string{
		"insertion_sequence", "id", "principal_id", "credential_id", "credential_fingerprint", "credential_revision",
		"admitted_at", "admission_class", "requested_name", "redacted_arguments", "server_id", "tool_id", "upstream_name",
		"descriptor_revision", "descriptor_fingerprint", "decision", "authorization_revision", "evaluated_at", "grant_id",
		"completed_at", "terminal_class",
	}
	rows, err := store.database.QueryContext(ctx, `SELECT name FROM pragma_table_info('invocations') ORDER BY cid`)
	if err != nil {
		return fmt.Errorf("read invocation columns: %w", err)
	}
	columns := make([]string, 0, len(expectedColumns))
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read invocation column: %w", err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate invocation columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close invocation columns: %w", err)
	}
	if len(columns) != len(expectedColumns) {
		return fmt.Errorf("invocations has %d columns", len(columns))
	}
	for index := range expectedColumns {
		if columns[index] != expectedColumns[index] {
			return fmt.Errorf("invocations column %d is %q", index, columns[index])
		}
	}

	var tableSQL string
	if err := store.database.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'invocations'`).Scan(&tableSQL); err != nil {
		return fmt.Errorf("read invocation table: %w", err)
	}
	normalizedTable := normalizeSchemaSQL(tableSQL)
	for _, fragment := range []string{
		"primary key autoincrement", "json_valid(redacted_arguments)", "length(cast(redacted_arguments as blob)) <= 8192",
		"server_id is null and tool_id is null", "admission_class = 'evaluated' and decision is not null",
		"completed_at is not null and terminal_class is not null", ") strict",
		"'invalid_params'", "'unknown_tool'", "'invalid_arguments'", "'authorization_unavailable'", "'evaluated'",
		"'prestart_failure'", "'succeeded'", "'downstream_failure'", "'outcome_unknown'",
	} {
		if !strings.Contains(normalizedTable, fragment) {
			return fmt.Errorf("invocation table is missing %q", fragment)
		}
	}

	var indexSQL, triggerSQL string
	if err := store.database.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = 'invocations_id'`).Scan(&indexSQL); err != nil {
		return fmt.Errorf("read invocation ID index: %w", err)
	}
	if normalizeSchemaSQL(indexSQL) != "create unique index invocations_id on invocations (id)" {
		return fmt.Errorf("invocation ID index does not match")
	}
	if err := store.database.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'trigger' AND name = 'invocations_terminal_once'`).Scan(&triggerSQL); err != nil {
		return fmt.Errorf("read invocation terminal trigger: %w", err)
	}
	normalizedTrigger := normalizeSchemaSQL(triggerSQL)
	for _, fragment := range []string{
		"before update on invocations", "old.completed_at is null", "old.terminal_class is null",
		"new.completed_at is not null", "new.terminal_class is not null", "raise(abort, 'invocation admission is immutable')",
	} {
		if !strings.Contains(normalizedTrigger, fragment) {
			return fmt.Errorf("invocation terminal trigger is missing %q", fragment)
		}
	}
	for _, column := range expectedColumns[:len(expectedColumns)-2] {
		if !strings.Contains(normalizedTrigger, "new."+column+" is old."+column) {
			return fmt.Errorf("invocation terminal trigger does not preserve %s", column)
		}
	}
	return nil
}

func (store *Store) verifyGrantRequestStructure(ctx context.Context) error {
	expectedColumns := []string{
		"insertion_sequence", "id", "principal_id", "state", "revision", "resolved_server_id", "resolved_upstream_name",
		"requested_scope", "requested_target", "requested_constraint", "requested_duration_seconds", "requested_future_tools_acknowledged",
		"dedupe_version", "dedupe_bytes", "submitted_evidence", "approved_scope", "approved_target", "approved_constraint",
		"approved_duration_seconds", "approved_future_tools_acknowledged", "approved_grant_id", "rejection_reason", "approved_evidence",
		"created_at", "updated_at", "closed_at",
	}
	if err := store.verifyTableColumns(ctx, "grant_request_identities", []string{"id", "created_at"}); err != nil {
		return err
	}
	if err := store.verifyTableColumns(ctx, "grant_requests", expectedColumns); err != nil {
		return err
	}
	if err := store.verifyTableColumns(ctx, "grant_request_evidence_bytes", []string{"singleton", "total_bytes"}); err != nil {
		return err
	}

	var identitySQL string
	if err := store.database.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'grant_request_identities'`).Scan(&identitySQL); err != nil {
		return fmt.Errorf("read grant request identity table: %w", err)
	}
	normalizedIdentity := normalizeSchemaSQL(identitySQL)
	for _, fragment := range []string{"id text primary key", "length(id) = 26", "created_at text not null", "strict, without rowid"} {
		if !strings.Contains(normalizedIdentity, fragment) {
			return fmt.Errorf("grant request identity table is missing %q", fragment)
		}
	}

	var tableSQL string
	if err := store.database.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'grant_requests'`).Scan(&tableSQL); err != nil {
		return fmt.Errorf("read grant request table: %w", err)
	}
	normalizedTable := normalizeSchemaSQL(tableSQL)
	for _, fragment := range []string{
		"primary key autoincrement", "state in ('pending', 'approved', 'rejected', 'cancelled')",
		"requested_scope in ('tool', 'server')", "length(dedupe_bytes) between 1 and 16384",
		"length(submitted_evidence) between 1 and 135168", "length(approved_evidence) between 1 and 135168",
		"between 60 and 2592000", "requested_future_tools_acknowledged in (0, 1)",
		"'not_approved', 'existing_access', 'scope_too_broad', 'policy_conflict'",
		"state <> 'pending'", "state <> 'approved'", "state <> 'rejected'", "state <> 'cancelled'", ") strict",
	} {
		if !strings.Contains(normalizedTable, fragment) {
			return fmt.Errorf("grant request table is missing %q", fragment)
		}
	}

	var aggregateSQL string
	if err := store.database.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'grant_request_evidence_bytes'`).Scan(&aggregateSQL); err != nil {
		return fmt.Errorf("read grant request evidence aggregate: %w", err)
	}
	normalizedAggregate := normalizeSchemaSQL(aggregateSQL)
	for _, fragment := range []string{"singleton = 1", "total_bytes between 0 and 268435456", "strict, without rowid"} {
		if !strings.Contains(normalizedAggregate, fragment) {
			return fmt.Errorf("grant request evidence aggregate is missing %q", fragment)
		}
	}
	var aggregateRows int
	var storedEvidenceBytes, calculatedEvidenceBytes int64
	if err := store.database.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(max(total_bytes), 0),
			(SELECT COALESCE(sum(COALESCE(length(submitted_evidence), 0) + COALESCE(length(approved_evidence), 0)), 0)
			 FROM grant_requests)
		FROM grant_request_evidence_bytes WHERE singleton = 1`).Scan(
		&aggregateRows, &storedEvidenceBytes, &calculatedEvidenceBytes,
	); err != nil {
		return fmt.Errorf("read grant request evidence aggregate singleton: %w", err)
	}
	if aggregateRows != 1 || storedEvidenceBytes != calculatedEvidenceBytes {
		return fmt.Errorf("grant request evidence aggregate does not match retained evidence")
	}

	expectedIndexes := map[string]string{
		"grant_requests_id":                "create unique index grant_requests_id on grant_requests (id)",
		"grant_requests_pending_dedupe":    "create unique index grant_requests_pending_dedupe on grant_requests (principal_id, dedupe_version, dedupe_bytes) where state = 'pending'",
		"grant_requests_principal_page":    "create index grant_requests_principal_page on grant_requests (principal_id, insertion_sequence)",
		"grant_requests_admin_page":        "create index grant_requests_admin_page on grant_requests (state, insertion_sequence)",
		"grant_requests_pending_principal": "create index grant_requests_pending_principal on grant_requests (principal_id) where state = 'pending'",
	}
	for name, expected := range expectedIndexes {
		var indexSQL string
		if err := store.database.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = ?`, name).Scan(&indexSQL); err != nil {
			return fmt.Errorf("read grant request index %s: %w", name, err)
		}
		if normalizeSchemaSQL(indexSQL) != expected {
			return fmt.Errorf("grant request index %s does not match", name)
		}
	}

	expectedTriggerFragments := map[string][]string{
		"grant_requests_terminal_once": {
			"before update on grant_requests", "old.state = 'pending'", "new.state in ('approved', 'rejected', 'cancelled')",
			"new.revision = old.revision + 1", "new.updated_at is new.closed_at",
			"raise(abort, 'grant request is immutable outside one terminal transition')",
		},
		"grant_requests_pending_not_deleted": {"before delete on grant_requests", "old.state = 'pending'", "raise(abort, 'pending grant request cannot be deleted')"},
		"grant_requests_evidence_insert":     {"after insert on grant_requests", "coalesce(length(new.submitted_evidence), 0)", "coalesce(length(new.approved_evidence), 0)"},
		"grant_requests_evidence_update":     {"after update of submitted_evidence, approved_evidence on grant_requests", "coalesce(length(old.submitted_evidence), 0)", "coalesce(length(new.approved_evidence), 0)"},
		"grant_requests_evidence_delete":     {"after delete on grant_requests", "coalesce(length(old.submitted_evidence), 0)", "coalesce(length(old.approved_evidence), 0)"},
	}
	var normalizedTerminalTrigger string
	for name, fragments := range expectedTriggerFragments {
		var triggerSQL string
		if err := store.database.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'trigger' AND name = ?`, name).Scan(&triggerSQL); err != nil {
			return fmt.Errorf("read grant request trigger %s: %w", name, err)
		}
		normalizedTrigger := normalizeSchemaSQL(triggerSQL)
		if name == "grant_requests_terminal_once" {
			normalizedTerminalTrigger = normalizedTrigger
		}
		for _, fragment := range fragments {
			if !strings.Contains(normalizedTrigger, fragment) {
				return fmt.Errorf("grant request trigger %s is missing %q", name, fragment)
			}
		}
	}
	for _, column := range expectedColumns {
		switch column {
		case "state", "revision", "approved_scope", "approved_target", "approved_constraint", "approved_duration_seconds", "approved_future_tools_acknowledged", "approved_grant_id", "rejection_reason", "approved_evidence", "updated_at", "closed_at":
			continue
		}
		if !strings.Contains(normalizedTerminalTrigger, "new."+column+" is old."+column) {
			return fmt.Errorf("grant request transition trigger does not preserve %s", column)
		}
	}
	return nil
}

func (store *Store) verifyTableColumns(ctx context.Context, table string, expected []string) error {
	rows, err := store.database.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return fmt.Errorf("read %s columns: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	columns := make([]string, 0, len(expected))
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return fmt.Errorf("read %s column: %w", table, err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s columns: %w", table, err)
	}
	if len(columns) != len(expected) {
		return fmt.Errorf("%s has %d columns", table, len(columns))
	}
	for index := range expected {
		if columns[index] != expected[index] {
			return fmt.Errorf("%s column %d is %q", table, index, columns[index])
		}
	}
	return nil
}

func normalizeSchemaSQL(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func inspectDatabase(ctx context.Context, path string) (int, error) {
	database, err := sql.Open("sqlite3", dataSource(path, true, false))
	if err != nil {
		return 0, fmt.Errorf("inspect Gateway database: %w", err)
	}
	defer func() { _ = database.Close() }()
	var applicationID, version int
	if err := database.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&applicationID); err != nil {
		return 0, fmt.Errorf("%w: read application ID: %w", ErrInvalidDatabase, err)
	}
	if applicationID != ApplicationID {
		return 0, fmt.Errorf("%w: application ID is %d", ErrInvalidDatabase, applicationID)
	}
	if err := database.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("%w: read schema version: %w", ErrInvalidDatabase, err)
	}
	return version, nil
}

func openConfigured(ctx context.Context, layout gatewaypaths.Layout, options testOptions) (*Store, error) {
	limit := options.databaseByteLimit
	if limit == 0 {
		limit = compiledDatabaseByteLimit()
	}
	database, err := sqliteDriver.Open(
		dataSource(layout.Database, false, true),
		func(connection *sqlite3.Conn) error {
			return configureConnectionSizeLimit(connection, limit)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("open Gateway database: %w", err)
	}
	database.SetMaxOpenConns(connectionLimit)
	database.SetMaxIdleConns(connectionLimit)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("open Gateway database: %w", err)
	}
	return &Store{
		database:      database,
		path:          layout.Database,
		marker:        newMutationMarker(layout, options.fault),
		mutationSlot:  make(chan struct{}, 1),
		fault:         options.fault,
		databaseLimit: limit,
	}, nil
}

func dataSource(path string, readOnly, durable bool) string {
	uri := &url.URL{Scheme: "file", Path: path}
	query := uri.Query()
	if readOnly {
		query.Set("mode", "ro")
	} else {
		query.Set("mode", "rw")
	}
	query.Add("_pragma", "busy_timeout("+strconv.Itoa(BusyTimeoutMilliseconds)+")")
	query.Add("_pragma", "foreign_keys(1)")
	if durable {
		query.Add("_pragma", "journal_mode(wal)")
		query.Add("_pragma", "synchronous(full)")
	}
	query.Set("_txlock", "immediate")
	uri.RawQuery = query.Encode()
	return uri.String()
}

func removeDatabaseGeneration(path string) {
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	_ = os.Remove(path)
}
