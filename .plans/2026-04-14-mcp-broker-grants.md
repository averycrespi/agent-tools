# MCP Broker Grant System Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Add a short-lived, argument-scoped authorization system to the MCP broker so a human operator can temporarily grant an agent the ability to call specific tools with bounded argument values.

**Architecture:** Grants live in a new `grants` SQLite table alongside the existing audit DB. Each grant binds one or more tools to a JSON Schema fragment their arguments must satisfy. Agents present grants via an `X-Grant-Token` HTTP header; the broker evaluates the grant **before** the rules engine and short-circuits to `Allow` on a match. On mismatch the request falls through to the existing rules engine unchanged — grants are additive-only. Audit records get `grant_id` and `grant_outcome` columns. Grants are authored via explicit per-constraint CLI flags (`--arg-equal`, `--arg-match`, `--arg-enum`) grouped by `--tool`, with `--arg-schema-file` as an escape hatch.

**Tech Stack:** Go 1.25, SQLite (`ncruces/go-sqlite3`, WASM), JSON Schema (`santhosh-tekuri/jsonschema/v6`, already indirect in `mcp-broker/go.mod`), Cobra + pflag for CLI, testify for tests.

**Design doc:** `.designs/2026-04-14-mcp-broker-grants.md`

---

## Phase layout

Each phase is an independently-shippable slice:

1. **Phase 1 — Grants package foundations.** Types, token generation, JSON Schema helpers, SQLite store, evaluation engine. All internal; no broker integration yet.
2. **Phase 2 — Broker integration.** Extend audit schema, carry the grant token in context, wire the engine into `broker.Handle()`.
3. **Phase 3 — HTTP API.** `POST/GET/DELETE /api/grants` mounted on the broker's root mux.
4. **Phase 4 — CLI.** `broker-cli grant create/list/revoke`.
5. **Phase 5 — Dashboard.** Read-only Grants tab, Audit tab grant pill/filter.
6. **Phase 6 — Documentation.** Update the two CLAUDE.md files.

Run `make audit` from the affected module before each commit (per `mcp-broker/CLAUDE.md` and `broker-cli/CLAUDE.md`).

---

## Phase 1 — Grants package foundations

### Task 1: Package skeleton and core types

**Files:**

- Create: `mcp-broker/internal/grants/types.go`

**Step 1: Create the file with exported types**

Paste the full contents below — no tests yet (pure types):

```go
// Package grants implements time-bounded, argument-scoped authorization
// grants that complement the static rules engine. See
// .designs/2026-04-14-mcp-broker-grants.md for the full design.
package grants

import (
	"encoding/json"
	"time"
)

// Outcome describes how the grants engine evaluated a request.
type Outcome string

const (
	// NotPresented indicates no X-Grant-Token header was present.
	NotPresented Outcome = ""
	// Invalid indicates a token was presented but did not correspond to an
	// active grant (unknown, expired, or revoked).
	Invalid Outcome = "invalid"
	// FellThrough indicates a valid grant was presented but no entry matched
	// the current tool and args.
	FellThrough Outcome = "fell_through"
	// Matched indicates an entry in a valid grant authorized the call.
	Matched Outcome = "matched"
)

// Entry binds a single tool to a JSON Schema its arguments must satisfy.
type Entry struct {
	Tool      string          `json:"tool"`
	ArgSchema json.RawMessage `json:"argSchema"`
}

// Grant is a persisted authorization record.
type Grant struct {
	ID          string    `json:"id"`
	Description string    `json:"description,omitempty"`
	Entries     []Entry   `json:"entries"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

// Active reports whether the grant is currently usable at time now.
func (g *Grant) Active(now time.Time) bool {
	if g.RevokedAt != nil {
		return false
	}
	return now.Before(g.ExpiresAt)
}

// Result is returned by Engine.Evaluate.
type Result struct {
	Outcome Outcome
	GrantID string // set for Matched and FellThrough; empty otherwise
}
```

**Step 2: Verify it compiles**

Run: `go build ./internal/grants/...` (from `mcp-broker/`)
Expected: exits 0 with no output.

**Step 3: Commit**

```bash
git add mcp-broker/internal/grants/types.go
git commit -m "feat(grants): add core types"
```

---

### Task 2: Token generation and hashing

**Files:**

- Create: `mcp-broker/internal/grants/token.go`
- Test: `mcp-broker/internal/grants/token_test.go`

**Step 1: Write the failing test**

`mcp-broker/internal/grants/token_test.go`:

```go
package grants

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewCredential(t *testing.T) {
	c1, err := NewCredential()
	require.NoError(t, err)
	c2, err := NewCredential()
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(c1.ID, "grt_"), "id must have grt_ prefix")
	require.True(t, strings.HasPrefix(c1.Token, "gr_"), "token must have gr_ prefix")
	require.Len(t, c1.ID, 4+12, "id = grt_ + 12 hex chars")
	require.NotEqual(t, c1.ID, c2.ID, "ids must be unique")
	require.NotEqual(t, c1.Token, c2.Token, "tokens must be unique")

	sum := sha256.Sum256([]byte(c1.Token))
	require.Equal(t, hex.EncodeToString(sum[:]), c1.TokenHash,
		"TokenHash must be the hex sha256 of Token")
}

func TestHashToken(t *testing.T) {
	got := HashToken("gr_known")
	sum := sha256.Sum256([]byte("gr_known"))
	require.Equal(t, hex.EncodeToString(sum[:]), got)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/grants/... -run TestNewCredential -v` (from `mcp-broker/`)
Expected: build failure — `undefined: NewCredential`, `undefined: HashToken`.

**Step 3: Write minimal implementation**

`mcp-broker/internal/grants/token.go`:

```go
package grants

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Credential is a freshly minted (id, token, token_hash) triple. The raw
// Token is shown to the operator exactly once at creation; only TokenHash
// is persisted.
type Credential struct {
	ID        string
	Token     string
	TokenHash string
}

// NewCredential mints a fresh grant credential. The ID is a short
// human-friendly handle safe to log; the Token is the secret presented
// on requests as X-Grant-Token.
func NewCredential() (Credential, error) {
	idBytes := make([]byte, 6) // 12 hex chars
	if _, err := rand.Read(idBytes); err != nil {
		return Credential{}, fmt.Errorf("generating grant id: %w", err)
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return Credential{}, fmt.Errorf("generating grant token: %w", err)
	}
	token := "gr_" + hex.EncodeToString(tokenBytes)
	return Credential{
		ID:        "grt_" + hex.EncodeToString(idBytes),
		Token:     token,
		TokenHash: HashToken(token),
	}, nil
}

// HashToken returns the hex-encoded SHA-256 of the given raw token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/grants/... -run TestNewCredential -v -race`
Expected: `PASS`.

**Step 5: Commit**

```bash
git add mcp-broker/internal/grants/token.go mcp-broker/internal/grants/token_test.go
git commit -m "feat(grants): generate credentials and hash tokens"
```

---

### Task 3: JSON Schema compile + validate helper

**Files:**

- Modify: `mcp-broker/go.mod` (promote `santhosh-tekuri/jsonschema/v6` from indirect to direct)
- Create: `mcp-broker/internal/grants/schema.go`
- Test: `mcp-broker/internal/grants/schema_test.go`

**Step 1: Write the failing test**

```go
package grants

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompileAndValidate(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"properties": {
			"branch": {"const": "feat/foo"},
			"force":  {"const": false}
		},
		"required": ["branch", "force"]
	}`)

	s, err := CompileSchema(raw)
	require.NoError(t, err)

	require.NoError(t, s.Validate(map[string]any{
		"branch": "feat/foo",
		"force":  false,
	}))

	require.Error(t, s.Validate(map[string]any{
		"branch": "main",
		"force":  false,
	}), "wrong branch value must not match")

	require.Error(t, s.Validate(map[string]any{
		"branch": "feat/foo",
	}), "missing required field must not match")
}

func TestCompileRejectsMalformed(t *testing.T) {
	_, err := CompileSchema(json.RawMessage(`{"type": 123}`))
	require.Error(t, err)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/grants/... -run TestCompileAndValidate -v` (from `mcp-broker/`)
Expected: `undefined: CompileSchema`.

**Step 3: Write minimal implementation**

`mcp-broker/internal/grants/schema.go`:

```go
package grants

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Schema is a compiled JSON Schema that can validate a decoded tool args map.
type Schema struct {
	compiled *jsonschema.Schema
}

// CompileSchema parses and compiles the given JSON Schema fragment.
func CompileSchema(raw json.RawMessage) (*Schema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("unmarshalling schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	const url = "grant://schema.json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("adding schema resource: %w", err)
	}
	s, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("compiling schema: %w", err)
	}
	return &Schema{compiled: s}, nil
}

// Validate reports whether args satisfies the schema.
func (s *Schema) Validate(args map[string]any) error {
	// jsonschema/v6 validates arbitrary any; convert nil to empty object
	// so "required" keywords fail cleanly on empty input.
	var v any = args
	if args == nil {
		v = map[string]any{}
	}
	return s.compiled.Validate(v)
}
```

**Step 4: Tidy go.mod**

Run: `cd mcp-broker && go mod tidy`
Expected: `santhosh-tekuri/jsonschema/v6` moves from indirect to direct in `go.mod`. `go.sum` unchanged.

**Step 5: Run test to verify it passes**

Run: `go test ./internal/grants/... -run TestCompileAndValidate -v -race` (from `mcp-broker/`)
Also run `TestCompileRejectsMalformed`: `go test ./internal/grants/... -v -race`
Expected: all PASS.

**Step 6: Commit**

```bash
git add mcp-broker/internal/grants/schema.go mcp-broker/internal/grants/schema_test.go mcp-broker/go.mod mcp-broker/go.sum
git commit -m "feat(grants): compile and validate JSON Schema fragments"
```

---

### Task 4: SQLite store — schema init

**Files:**

- Create: `mcp-broker/internal/grants/store.go`
- Test: `mcp-broker/internal/grants/store_test.go`

**Step 1: Write the failing test**

```go
package grants

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/stretchr/testify/require"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "grants.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestNewStoreCreatesSchema(t *testing.T) {
	db := openTestDB(t)
	_, err := NewStore(context.Background(), db)
	require.NoError(t, err)

	row := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='grants'`)
	var name string
	require.NoError(t, row.Scan(&name))
	require.Equal(t, "grants", name)
}

func TestNewStoreIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	_, err := NewStore(context.Background(), db)
	require.NoError(t, err)
	_, err = NewStore(context.Background(), db)
	require.NoError(t, err, "running NewStore twice must not error")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/grants/... -run TestNewStore -v`
Expected: `undefined: NewStore`.

**Step 3: Write minimal implementation**

`mcp-broker/internal/grants/store.go`:

```go
package grants

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// Store persists grants in SQLite.
type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewStore opens or creates the grants table in db.
func NewStore(ctx context.Context, db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.init(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

const grantsSchema = `
CREATE TABLE IF NOT EXISTS grants (
	id          TEXT PRIMARY KEY,
	token_hash  TEXT NOT NULL UNIQUE,
	description TEXT,
	entries     TEXT NOT NULL,
	created_at  INTEGER NOT NULL,
	expires_at  INTEGER NOT NULL,
	revoked_at  INTEGER
);
CREATE INDEX IF NOT EXISTS idx_grants_token_hash ON grants(token_hash);
CREATE INDEX IF NOT EXISTS idx_grants_expires_at ON grants(expires_at);
`

func (s *Store) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, grantsSchema); err != nil {
		return fmt.Errorf("initializing grants schema: %w", err)
	}
	return nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/grants/... -run TestNewStore -v -race`
Expected: PASS.

**Step 5: Commit**

```bash
git add mcp-broker/internal/grants/store.go mcp-broker/internal/grants/store_test.go
git commit -m "feat(grants): initialize SQLite schema"
```

---

### Task 5: Store — create, lookup, list, revoke

**Files:**

- Modify: `mcp-broker/internal/grants/store.go`
- Modify: `mcp-broker/internal/grants/store_test.go`

**Step 1: Write the failing tests** (append to `store_test.go`)

```go
func TestStoreCreateAndLookup(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	store, err := NewStore(ctx, db)
	require.NoError(t, err)

	cred, err := NewCredential()
	require.NoError(t, err)

	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	g := Grant{
		ID:          cred.ID,
		Description: "push feat/foo",
		Entries: []Entry{{
			Tool:      "git.git_push",
			ArgSchema: json.RawMessage(`{"type":"object"}`),
		}},
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	require.NoError(t, store.Create(ctx, g, cred.TokenHash))

	got, err := store.LookupByTokenHash(ctx, cred.TokenHash)
	require.NoError(t, err)
	require.Equal(t, cred.ID, got.ID)
	require.Equal(t, "push feat/foo", got.Description)
	require.Len(t, got.Entries, 1)
	require.Equal(t, "git.git_push", got.Entries[0].Tool)
	require.True(t, got.CreatedAt.Equal(now))
	require.True(t, got.ExpiresAt.Equal(now.Add(time.Hour)))
	require.Nil(t, got.RevokedAt)
}

func TestStoreLookupUnknown(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(context.Background(), db)
	require.NoError(t, err)

	g, err := store.LookupByTokenHash(context.Background(), "deadbeef")
	require.NoError(t, err)
	require.Nil(t, g, "unknown token_hash must return (nil, nil)")
}

func TestStoreRevokeIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	store, err := NewStore(ctx, db)
	require.NoError(t, err)

	cred, _ := NewCredential()
	now := time.Now().UTC()
	g := Grant{
		ID:        cred.ID,
		Entries:   []Entry{{Tool: "x.y", ArgSchema: json.RawMessage(`{}`)}},
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	require.NoError(t, store.Create(ctx, g, cred.TokenHash))

	require.NoError(t, store.Revoke(ctx, g.ID, now.Add(time.Minute)))
	require.NoError(t, store.Revoke(ctx, g.ID, now.Add(2*time.Minute)),
		"revoking an already-revoked grant must not error")

	got, err := store.LookupByTokenHash(ctx, cred.TokenHash)
	require.NoError(t, err)
	require.NotNil(t, got.RevokedAt, "RevokedAt must be set after revoke")
}

func TestStoreListActive(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	store, err := NewStore(ctx, db)
	require.NoError(t, err)
	now := time.Now().UTC()

	active, _ := NewCredential()
	expired, _ := NewCredential()
	revoked, _ := NewCredential()

	mk := func(id string, expiresIn time.Duration) Grant {
		return Grant{
			ID:        id,
			Entries:   []Entry{{Tool: "x.y", ArgSchema: json.RawMessage(`{}`)}},
			CreatedAt: now,
			ExpiresAt: now.Add(expiresIn),
		}
	}
	require.NoError(t, store.Create(ctx, mk(active.ID, time.Hour), active.TokenHash))
	require.NoError(t, store.Create(ctx, mk(expired.ID, -time.Hour), expired.TokenHash))
	require.NoError(t, store.Create(ctx, mk(revoked.ID, time.Hour), revoked.TokenHash))
	require.NoError(t, store.Revoke(ctx, revoked.ID, now))

	got, err := store.List(ctx, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, active.ID, got[0].ID)

	all, err := store.List(ctx, true)
	require.NoError(t, err)
	require.Len(t, all, 3)
}
```

Add imports: `encoding/json`, `time`.

**Step 2: Run the tests to verify they fail**

Run: `go test ./internal/grants/... -run TestStore -v`
Expected: build failure — `undefined: Store.Create`, `LookupByTokenHash`, `Revoke`, `List`.

**Step 3: Add the CRUD methods to `store.go`**

Append:

```go
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Create persists g. tokenHash must be the SHA-256 of the raw token the
// caller will hand to the operator.
func (s *Store) Create(ctx context.Context, g Grant, tokenHash string) error {
	entriesJSON, err := json.Marshal(g.Entries)
	if err != nil {
		return fmt.Errorf("marshalling entries: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO grants (id, token_hash, description, entries, created_at, expires_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL)
	`, g.ID, tokenHash, g.Description, string(entriesJSON),
		g.CreatedAt.UnixMilli(), g.ExpiresAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("inserting grant: %w", err)
	}
	return nil
}

// LookupByTokenHash returns the grant with the given token_hash, or
// (nil, nil) if none exists.
func (s *Store) LookupByTokenHash(ctx context.Context, tokenHash string) (*Grant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRowContext(ctx, `
		SELECT id, description, entries, created_at, expires_at, revoked_at
		FROM grants WHERE token_hash = ?
	`, tokenHash)
	g, err := scanGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}

// LookupByID returns the grant with the given id, or (nil, nil) if none exists.
func (s *Store) LookupByID(ctx context.Context, id string) (*Grant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRowContext(ctx, `
		SELECT id, description, entries, created_at, expires_at, revoked_at
		FROM grants WHERE id = ?
	`, id)
	g, err := scanGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}

// List returns grants ordered by created_at DESC. When includeInactive is
// false, expired and revoked grants are omitted.
func (s *Store) List(ctx context.Context, includeInactive bool) ([]Grant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var (
		rows *sql.Rows
		err  error
	)
	if includeInactive {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, description, entries, created_at, expires_at, revoked_at
			FROM grants ORDER BY created_at DESC
		`)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, description, entries, created_at, expires_at, revoked_at
			FROM grants
			WHERE revoked_at IS NULL AND expires_at > ?
			ORDER BY created_at DESC
		`, time.Now().UnixMilli())
	}
	if err != nil {
		return nil, fmt.Errorf("querying grants: %w", err)
	}
	defer rows.Close()

	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// Revoke sets revoked_at for the given grant id. Idempotent: revoking an
// already-revoked grant is a no-op.
func (s *Store) Revoke(ctx context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `
		UPDATE grants SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL
	`, at.UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("revoking grant: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanGrant(r rowScanner) (*Grant, error) {
	var (
		g           Grant
		entriesRaw  string
		createdMs   int64
		expiresMs   int64
		revokedMs   sql.NullInt64
	)
	if err := r.Scan(&g.ID, &g.Description, &entriesRaw, &createdMs, &expiresMs, &revokedMs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(entriesRaw), &g.Entries); err != nil {
		return nil, fmt.Errorf("unmarshalling entries: %w", err)
	}
	g.CreatedAt = time.UnixMilli(createdMs).UTC()
	g.ExpiresAt = time.UnixMilli(expiresMs).UTC()
	if revokedMs.Valid {
		t := time.UnixMilli(revokedMs.Int64).UTC()
		g.RevokedAt = &t
	}
	return &g, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/grants/... -v -race` (from `mcp-broker/`)
Expected: all PASS.

**Step 5: Run full module audit**

Run: `make audit` (from `mcp-broker/`)
Expected: PASS.

**Step 6: Commit**

```bash
git add mcp-broker/internal/grants/store.go mcp-broker/internal/grants/store_test.go
git commit -m "feat(grants): store CRUD for grants"
```

---

### Task 6: Grants engine — Evaluate

**Files:**

- Create: `mcp-broker/internal/grants/engine.go`
- Test: `mcp-broker/internal/grants/engine_test.go`

**Step 1: Write the failing test**

```go
package grants

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mkGrant(t *testing.T, store *Store, ttl time.Duration, entries []Entry) (grantID, token string) {
	t.Helper()
	cred, err := NewCredential()
	require.NoError(t, err)
	now := time.Now().UTC()
	g := Grant{
		ID:        cred.ID,
		Entries:   entries,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	require.NoError(t, store.Create(context.Background(), g, cred.TokenHash))
	return cred.ID, cred.Token
}

func TestEngineEvaluate(t *testing.T) {
	store, err := NewStore(context.Background(), openTestDB(t))
	require.NoError(t, err)
	eng := NewEngine(store)

	pushSchema := json.RawMessage(`{
		"type":"object",
		"properties":{"branch":{"const":"feat/foo"},"force":{"const":false}},
		"required":["branch","force"]
	}`)
	grantID, token := mkGrant(t, store, time.Hour, []Entry{
		{Tool: "git.git_push", ArgSchema: pushSchema},
		{Tool: "git.git_fetch", ArgSchema: json.RawMessage(`{"type":"object"}`)},
	})

	ctx := context.Background()

	t.Run("not presented", func(t *testing.T) {
		r, err := eng.Evaluate(ctx, "", "git.git_push", map[string]any{"branch": "feat/foo", "force": false})
		require.NoError(t, err)
		require.Equal(t, NotPresented, r.Outcome)
		require.Empty(t, r.GrantID)
	})

	t.Run("invalid token", func(t *testing.T) {
		r, err := eng.Evaluate(ctx, "gr_bogus", "git.git_push", nil)
		require.NoError(t, err)
		require.Equal(t, Invalid, r.Outcome)
		require.Empty(t, r.GrantID)
	})

	t.Run("matched", func(t *testing.T) {
		r, err := eng.Evaluate(ctx, token, "git.git_push", map[string]any{"branch": "feat/foo", "force": false})
		require.NoError(t, err)
		require.Equal(t, Matched, r.Outcome)
		require.Equal(t, grantID, r.GrantID)
	})

	t.Run("matched open-schema entry", func(t *testing.T) {
		r, err := eng.Evaluate(ctx, token, "git.git_fetch", map[string]any{"remote": "origin"})
		require.NoError(t, err)
		require.Equal(t, Matched, r.Outcome)
	})

	t.Run("fell through — wrong args", func(t *testing.T) {
		r, err := eng.Evaluate(ctx, token, "git.git_push", map[string]any{"branch": "main", "force": false})
		require.NoError(t, err)
		require.Equal(t, FellThrough, r.Outcome)
		require.Equal(t, grantID, r.GrantID)
	})

	t.Run("fell through — wrong tool", func(t *testing.T) {
		r, err := eng.Evaluate(ctx, token, "foo.bar", map[string]any{})
		require.NoError(t, err)
		require.Equal(t, FellThrough, r.Outcome)
		require.Equal(t, grantID, r.GrantID)
	})
}

func TestEngineExpiredIsInvalid(t *testing.T) {
	store, err := NewStore(context.Background(), openTestDB(t))
	require.NoError(t, err)
	eng := NewEngine(store)

	_, token := mkGrant(t, store, -time.Hour, []Entry{ // already expired
		{Tool: "x.y", ArgSchema: json.RawMessage(`{}`)},
	})

	r, err := eng.Evaluate(context.Background(), token, "x.y", nil)
	require.NoError(t, err)
	require.Equal(t, Invalid, r.Outcome)
}

func TestEngineRevokedIsInvalid(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, openTestDB(t))
	require.NoError(t, err)
	eng := NewEngine(store)

	id, token := mkGrant(t, store, time.Hour, []Entry{
		{Tool: "x.y", ArgSchema: json.RawMessage(`{}`)},
	})
	require.NoError(t, store.Revoke(ctx, id, time.Now().UTC()))

	r, err := eng.Evaluate(ctx, token, "x.y", nil)
	require.NoError(t, err)
	require.Equal(t, Invalid, r.Outcome)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/grants/... -run TestEngine -v`
Expected: `undefined: NewEngine`.

**Step 3: Write minimal implementation**

`mcp-broker/internal/grants/engine.go`:

```go
package grants

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Engine evaluates grant tokens against tool calls.
type Engine struct {
	store *Store

	mu    sync.Mutex
	cache map[string]*compiledGrant // grant id → compiled schemas
}

type compiledGrant struct {
	entries []compiledEntry
}

type compiledEntry struct {
	tool   string
	schema *Schema
}

// NewEngine constructs an Engine backed by the given store.
func NewEngine(store *Store) *Engine {
	return &Engine{
		store: store,
		cache: map[string]*compiledGrant{},
	}
}

// Evaluate inspects the presented token (which may be empty) and returns
// the grant evaluation result for a call to tool with the given args.
func (e *Engine) Evaluate(ctx context.Context, token, tool string, args map[string]any) (Result, error) {
	if token == "" {
		return Result{Outcome: NotPresented}, nil
	}
	g, err := e.store.LookupByTokenHash(ctx, HashToken(token))
	if err != nil {
		return Result{}, fmt.Errorf("looking up grant: %w", err)
	}
	if g == nil || !g.Active(time.Now().UTC()) {
		return Result{Outcome: Invalid}, nil
	}
	cg, err := e.compile(g)
	if err != nil {
		// A compile error on a stored grant is a logic bug; treat as invalid
		// rather than blocking the request (grants are additive-only).
		return Result{Outcome: Invalid, GrantID: g.ID}, nil
	}
	for _, entry := range cg.entries {
		if entry.tool != tool {
			continue
		}
		if entry.schema.Validate(args) == nil {
			return Result{Outcome: Matched, GrantID: g.ID}, nil
		}
	}
	return Result{Outcome: FellThrough, GrantID: g.ID}, nil
}

// Invalidate drops the cached compiled form for a grant id. Call after revoke.
func (e *Engine) Invalidate(grantID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.cache, grantID)
}

func (e *Engine) compile(g *Grant) (*compiledGrant, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cg, ok := e.cache[g.ID]; ok {
		return cg, nil
	}
	cg := &compiledGrant{entries: make([]compiledEntry, 0, len(g.Entries))}
	for _, entry := range g.Entries {
		s, err := CompileSchema(entry.ArgSchema)
		if err != nil {
			return nil, fmt.Errorf("compiling schema for tool %q: %w", entry.Tool, err)
		}
		cg.entries = append(cg.entries, compiledEntry{tool: entry.Tool, schema: s})
	}
	e.cache[g.ID] = cg
	return cg, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/grants/... -v -race`
Expected: all PASS.

**Step 5: Commit**

```bash
git add mcp-broker/internal/grants/engine.go mcp-broker/internal/grants/engine_test.go
git commit -m "feat(grants): evaluate tokens against tool calls"
```

---

## Phase 2 — Broker integration

### Task 7: Extend audit schema and `Record` struct

**Files:**

- Modify: `mcp-broker/internal/audit/audit.go`
- Test: `mcp-broker/internal/audit/audit_test.go`

**Step 1: Read the current audit schema location**

Open `mcp-broker/internal/audit/audit.go` and find the `CREATE TABLE` block (around lines 35-48) and the migration `ALTER TABLE` calls (around line 84). New columns should be added in the same permissive style (bare `ALTER TABLE` with errors ignored, matching the existing pattern per `mcp-broker/CLAUDE.md`).

**Step 2: Write the failing test** (append to `audit_test.go`)

```go
func TestRecordWithGrant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	auditor, err := New(path, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auditor.Close() })

	ctx := context.Background()
	require.NoError(t, auditor.Record(ctx, Record{
		Timestamp:    time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC),
		Tool:         "git.git_push",
		Args:         map[string]any{"branch": "feat/foo"},
		Verdict:      "allow",
		GrantID:      "grt_abc",
		GrantOutcome: "matched",
	}))

	records, err := auditor.Query(ctx, QueryFilter{})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "grt_abc", records[0].GrantID)
	require.Equal(t, "matched", records[0].GrantOutcome)
}

func TestAuditSchemaIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	// first open creates the schema
	a1, err := New(path, nil)
	require.NoError(t, err)
	require.NoError(t, a1.Close())
	// second open must not error (columns already exist)
	a2, err := New(path, nil)
	require.NoError(t, err)
	require.NoError(t, a2.Close())
}
```

> The test references `QueryFilter` and `Query`; use whatever the existing test file calls the query function. If the audit package does not expose a generic query, add lookups equivalent to what the dashboard's audit tab already uses (check `internal/dashboard/dashboard.go` `handleAudit` for the shape).

**Step 3: Run test to verify it fails**

Run: `go test ./internal/audit/... -run TestRecordWithGrant -v`
Expected: unknown fields `GrantID` and `GrantOutcome`.

**Step 4: Extend the `Record` struct**

In `mcp-broker/internal/audit/audit.go`, add to `Record`:

```go
GrantID      string `json:"grant_id,omitempty"`
GrantOutcome string `json:"grant_outcome,omitempty"`
```

Add matching columns in the schema's `CREATE TABLE` block (safe because `CREATE TABLE IF NOT EXISTS` is a no-op on existing DBs — the migration handles old DBs):

```sql
grant_id      TEXT,
grant_outcome TEXT,
```

Add the migration for older DBs right after the `CREATE TABLE` call (follow the existing permissive-ALTER pattern — errors on "duplicate column" are acceptable):

```go
_, _ = db.Exec(`ALTER TABLE audit_records ADD COLUMN grant_id TEXT`)
_, _ = db.Exec(`ALTER TABLE audit_records ADD COLUMN grant_outcome TEXT`)
_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_grant_id ON audit_records(grant_id)`)
```

Extend the `INSERT` statement and its binding to include both new columns, and extend the `SELECT` list used by `Query` (or equivalent) to scan them back into the struct.

**Step 5: Run tests to verify they pass**

Run: `go test ./internal/audit/... -v -race`
Expected: all PASS.

**Step 6: Commit**

```bash
git add mcp-broker/internal/audit/audit.go mcp-broker/internal/audit/audit_test.go
git commit -m "feat(audit): add grant_id and grant_outcome columns"
```

---

### Task 8: Context plumbing for grant tokens

**Files:**

- Create: `mcp-broker/internal/grants/context.go`
- Test: `mcp-broker/internal/grants/context_test.go`

**Step 1: Write the failing test**

```go
package grants

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContextToken(t *testing.T) {
	ctx := context.Background()
	require.Empty(t, TokenFromContext(ctx))

	ctx = ContextWithToken(ctx, "gr_abc")
	require.Equal(t, "gr_abc", TokenFromContext(ctx))
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/grants/... -run TestContextToken -v`
Expected: `undefined: ContextWithToken`.

**Step 3: Implement**

`mcp-broker/internal/grants/context.go`:

```go
package grants

import "context"

type ctxKey struct{}

// ContextWithToken returns ctx annotated with the raw grant token string.
func ContextWithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, ctxKey{}, token)
}

// TokenFromContext returns the raw grant token set by ContextWithToken, or
// the empty string if none was set.
func TokenFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKey{}).(string)
	return v
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/grants/... -run TestContextToken -v -race`
Expected: PASS.

**Step 5: Commit**

```bash
git add mcp-broker/internal/grants/context.go mcp-broker/internal/grants/context_test.go
git commit -m "feat(grants): plumb token through request context"
```

---

### Task 9: HTTP middleware to read `X-Grant-Token`

**Files:**

- Modify: `mcp-broker/internal/auth/auth.go` (or create a new file in the same package)
- Test: modify `mcp-broker/internal/auth/auth_test.go`

> The middleware is stateless and token-type-agnostic, so it lives alongside the existing bearer `Middleware` in the `auth` package. It does not authenticate — it just stashes the header for later consultation.

**Step 1: Write the failing test**

Add to `mcp-broker/internal/auth/auth_test.go`:

```go
func TestGrantTokenMiddleware(t *testing.T) {
	var got string
	h := GrantTokenMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = grants.TokenFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("X-Grant-Token", "gr_abc")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "gr_abc", got)
}
```

Add imports: `"github.com/<module-path>/mcp-broker/internal/grants"` — look up the exact module path in `mcp-broker/go.mod` and use it verbatim.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/auth/... -run TestGrantTokenMiddleware -v`
Expected: `undefined: GrantTokenMiddleware`.

**Step 3: Implement**

In the auth package (new function in `auth.go` is fine):

```go
// GrantTokenMiddleware reads X-Grant-Token from the request and, if
// present, attaches it to the request context via grants.ContextWithToken.
// Absence of the header is not an error: downstream code treats an empty
// token as "no grant presented."
func GrantTokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t := r.Header.Get("X-Grant-Token"); t != "" {
			r = r.WithContext(grants.ContextWithToken(r.Context(), t))
		}
		next.ServeHTTP(w, r)
	})
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/auth/... -v -race`
Expected: PASS.

**Step 5: Commit**

```bash
git add mcp-broker/internal/auth/auth.go mcp-broker/internal/auth/auth_test.go
git commit -m "feat(auth): stash X-Grant-Token in request context"
```

---

### Task 10: Wire grants engine into `broker.Handle()`

**Files:**

- Modify: `mcp-broker/internal/broker/broker.go`
- Modify: the `Broker` struct constructor (likely `New(...)`) in the same file — find it by searching for `func New` near the top
- Test: `mcp-broker/internal/broker/broker_test.go` (create if absent; otherwise extend)

**Step 1: Understand the existing `Handle` shape**

Re-read `Handle` (around line 57 per the survey). Note that it takes `ctx, tool, args` and calls `b.rules.Evaluate(tool)` followed by switch on `verdict`. The grant check slots in between reading args and the rules call, using `grants.TokenFromContext(ctx)`.

**Step 2: Write the failing test**

Table-driven test covering the four grant outcomes. Add the following test (adapt existing broker test scaffolding if present — reuse fakes for `ServerManager`, `Approver`, `Auditor`):

```go
func TestHandleGrantMatchedSkipsRulesAndApproval(t *testing.T) {
	// Set up: rules say "deny", but a matching grant authorizes the call.
	// Expect: call proceeds; audit record has GrantOutcome="matched".

	ctx := context.Background()
	db := openInMemSQLite(t)
	store, _ := grants.NewStore(ctx, db)
	eng := grants.NewEngine(store)

	cred, _ := grants.NewCredential()
	now := time.Now().UTC()
	require.NoError(t, store.Create(ctx, grants.Grant{
		ID:        cred.ID,
		Entries:   []grants.Entry{{Tool: "x.y", ArgSchema: json.RawMessage(`{"type":"object"}`)}},
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}, cred.TokenHash))

	fakeServers := &fakeServerManager{resp: "ok"}
	fakeAuditor := &fakeAuditor{}
	denyRules, _ := rules.New([]config.RuleConfig{{Tool: "x.y", Verdict: "deny"}})

	b := New(denyRules, fakeServers, fakeAuditor, nilApprover{}, eng)

	ctx = grants.ContextWithToken(ctx, cred.Token)
	got, err := b.Handle(ctx, "x.y", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, "ok", got)

	require.Len(t, fakeAuditor.records, 1)
	require.Equal(t, "matched", fakeAuditor.records[0].GrantOutcome)
	require.Equal(t, cred.ID, fakeAuditor.records[0].GrantID)
}

func TestHandleGrantFellThroughAppliesRules(t *testing.T) {
	// A valid token is presented, but args don't match; rules should
	// evaluate as usual and the record should note grant_outcome=fell_through.
	// (Concrete setup mirrors the matched case; args are wrong.)
	// Intentional sketch — fill in identically to the matched test above,
	// but pass args that fail the schema.
}

func TestHandleInvalidTokenDoesNotDeny(t *testing.T) {
	// Invalid token presented. Rules engine decides (e.g. allow).
	// Audit record has grant_outcome=invalid; call still succeeds.
}
```

Flesh out the two sketch tests using the same pattern. Use existing fakes where present; only add new ones if no equivalent exists.

**Step 3: Run tests to verify they fail**

Run: `go test ./internal/broker/... -run TestHandleGrant -v`
Expected: compile failure — `New` does not accept an engine; `Record.GrantOutcome` missing if Task 7 not yet merged (it is — we're sequential).

**Step 4: Modify the `Broker` struct and `New` constructor**

Add an `engine *grants.Engine` field to `Broker` and accept it as the last parameter to `New`. Every caller of `New` in `cmd/mcp-broker` and `test/e2e` must be updated. Search with `git grep 'broker.New('` to find call sites.

Inject the engine at the MCP serve setup (in `cmd/mcp-broker/serve.go` — the survey points at line 134 for `Handle` invocation; find the surrounding setup and construct the engine there from the shared sql.DB).

**Step 5: Extend `Handle()`**

In `Handle`, immediately after building the initial `rec`:

```go
token := grants.TokenFromContext(ctx)
gr, err := b.engine.Evaluate(ctx, token, tool, args)
if err != nil {
	// Do not block the call on grant lookup errors; log and continue.
	if b.logger != nil {
		b.logger.Warn("grant evaluation failed", "err", err)
	}
	gr = grants.Result{Outcome: grants.NotPresented}
}
rec.GrantID = gr.GrantID
rec.GrantOutcome = string(gr.Outcome)

if gr.Outcome == grants.Matched {
	rec.Verdict = rules.Allow.String()
	result, err := b.servers.Call(ctx, tool, args)
	if err != nil {
		rec.Error = err.Error()
	}
	_ = b.auditor.Record(ctx, rec)
	return result, err
}
```

Leave the existing rules-engine flow untouched for the non-matched cases.

**Step 6: Run tests to verify they pass**

Run: `go test ./internal/broker/... -v -race`
Expected: all PASS.

**Step 7: Full module audit**

Run: `make audit` (from `mcp-broker/`)
Expected: PASS.

**Step 8: Commit**

```bash
git add mcp-broker/internal/broker/ mcp-broker/cmd/mcp-broker/
git commit -m "feat(broker): evaluate grants before rules"
```

---

### Task 11: Wire grants middleware into serve.go

**Files:**

- Modify: `mcp-broker/cmd/mcp-broker/serve.go`

**Step 1: Locate the existing auth wiring**

The survey cites `serve.go:154` — `auth.Middleware(token, next)` wraps the mux. Find that block.

**Step 2: Wrap with the grant-token middleware**

Replace:

```go
handler := auth.Middleware(authToken, mux)
```

with:

```go
handler := auth.Middleware(authToken, auth.GrantTokenMiddleware(mux))
```

The order matters: the bearer auth check runs first (unauthenticated requests never touch the grant layer); the grant middleware unconditionally stashes `X-Grant-Token` for any authenticated request.

**Step 3: Construct the engine and thread it into the broker**

Just before broker construction, add:

```go
grantStore, err := grants.NewStore(ctx, auditDB) // share the audit DB connection
if err != nil {
	return fmt.Errorf("initializing grant store: %w", err)
}
grantEngine := grants.NewEngine(grantStore)
```

Pass `grantEngine` (and, for Phase 3, `grantStore`) to the broker constructor updated in Task 10.

> The grant store is the same `*sql.DB` the audit package uses. They share the file; the two `CREATE TABLE IF NOT EXISTS` calls coexist cleanly.

**Step 4: Smoke test**

Run: `go build ./cmd/mcp-broker/...` (from `mcp-broker/`)
Expected: compiles.

Run the existing e2e suite:

```bash
make test-e2e
```

Expected: PASS. No new e2e coverage needed yet — Phase 3 adds the API.

**Step 5: Commit**

```bash
git add mcp-broker/cmd/mcp-broker/serve.go
git commit -m "feat(broker): mount grants middleware in serve"
```

---

## Phase 3 — HTTP API

### Task 12: `POST /api/grants` handler

**Files:**

- Create: `mcp-broker/internal/grants/api.go`
- Test: `mcp-broker/internal/grants/api_test.go`

**Step 1: Write the failing test**

```go
package grants

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCreateGrantEndpoint(t *testing.T) {
	store, err := NewStore(context.Background(), openTestDB(t))
	require.NoError(t, err)
	api := NewAPI(store, NewEngine(store))

	body := CreateRequest{
		Description: "push feat/foo",
		TTL:         Duration(time.Hour),
		Entries: []Entry{
			{Tool: "git.git_push", ArgSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/grants", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)

	var resp CreateResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.NotEmpty(t, resp.ID)
	require.NotEmpty(t, resp.Token, "raw token must be returned exactly once")
	require.NotZero(t, resp.ExpiresAt)

	// Stored grant must exist and be active.
	g, err := store.LookupByTokenHash(context.Background(), HashToken(resp.Token))
	require.NoError(t, err)
	require.NotNil(t, g)
	require.Equal(t, resp.ID, g.ID)
}

func TestCreateGrantRejectsBadSchema(t *testing.T) {
	store, _ := NewStore(context.Background(), openTestDB(t))
	api := NewAPI(store, NewEngine(store))

	body := CreateRequest{
		TTL: Duration(time.Hour),
		Entries: []Entry{
			{Tool: "git.git_push", ArgSchema: json.RawMessage(`{"type": 123}`)}, // invalid
		},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/grants", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/grants/... -run TestCreateGrant -v`
Expected: `undefined: NewAPI`.

**Step 3: Implement**

`mcp-broker/internal/grants/api.go`:

```go
package grants

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Duration wraps time.Duration with JSON support using Go's duration syntax.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	if bytes := string(b); len(bytes) > 0 && bytes[0] == '"' {
		s, err := strconvUnquote(bytes)
		if err != nil {
			return err
		}
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("parsing duration %q: %w", s, err)
		}
		*d = Duration(parsed)
		return nil
	}
	// numeric — treat as nanoseconds
	var ns int64
	if err := json.Unmarshal(b, &ns); err != nil {
		return err
	}
	*d = Duration(time.Duration(ns))
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func strconvUnquote(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", errors.New("not a quoted string")
	}
	return s[1 : len(s)-1], nil
}

// CreateRequest is the JSON body for POST /api/grants.
type CreateRequest struct {
	Description string   `json:"description,omitempty"`
	TTL         Duration `json:"ttl"`
	Entries     []Entry  `json:"entries"`
}

// CreateResponse is the JSON body returned from POST /api/grants.
// Token is the raw bearer string, shown exactly once.
type CreateResponse struct {
	ID          string    `json:"id"`
	Token       string    `json:"token"`
	Description string    `json:"description,omitempty"`
	Tools       []string  `json:"tools"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// API wires the HTTP handlers for /api/grants*.
type API struct {
	store  *Store
	engine *Engine
}

func NewAPI(store *Store, engine *Engine) *API {
	return &API{store: store, engine: engine}
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/grants":
		a.handleCreate(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/grants":
		a.handleList(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/grants/"):
		a.handleRevoke(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *API) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decoding request: %v", err), http.StatusBadRequest)
		return
	}
	if time.Duration(req.TTL) <= 0 {
		http.Error(w, "ttl must be positive", http.StatusBadRequest)
		return
	}
	if len(req.Entries) == 0 {
		http.Error(w, "at least one entry required", http.StatusBadRequest)
		return
	}
	for _, e := range req.Entries {
		if _, err := CompileSchema(e.ArgSchema); err != nil {
			http.Error(w, fmt.Sprintf("entry %q: %v", e.Tool, err), http.StatusBadRequest)
			return
		}
	}

	cred, err := NewCredential()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	g := Grant{
		ID:          cred.ID,
		Description: req.Description,
		Entries:     req.Entries,
		CreatedAt:   now,
		ExpiresAt:   now.Add(time.Duration(req.TTL)),
	}
	if err := a.store.Create(r.Context(), g, cred.TokenHash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tools := make([]string, len(g.Entries))
	for i, e := range g.Entries {
		tools[i] = e.Tool
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(CreateResponse{
		ID:          g.ID,
		Token:       cred.Token,
		Description: g.Description,
		Tools:       tools,
		CreatedAt:   g.CreatedAt,
		ExpiresAt:   g.ExpiresAt,
	})
}
```

**Step 4: Run the create-only tests**

Run: `go test ./internal/grants/... -run TestCreateGrant -v -race`
Expected: PASS.

**Step 5: Commit**

```bash
git add mcp-broker/internal/grants/api.go mcp-broker/internal/grants/api_test.go
git commit -m "feat(grants): POST /api/grants"
```

---

### Task 13: `GET /api/grants` handler

**Files:**

- Modify: `mcp-broker/internal/grants/api.go`
- Modify: `mcp-broker/internal/grants/api_test.go`

**Step 1: Write the failing test**

```go
func TestListGrantsEndpoint(t *testing.T) {
	store, _ := NewStore(context.Background(), openTestDB(t))
	api := NewAPI(store, NewEngine(store))

	// seed two grants, revoke one
	for _, desc := range []string{"keep", "revoke"} {
		cred, _ := NewCredential()
		now := time.Now().UTC()
		require.NoError(t, store.Create(context.Background(), Grant{
			ID:          cred.ID,
			Description: desc,
			Entries:     []Entry{{Tool: "x.y", ArgSchema: json.RawMessage(`{}`)}},
			CreatedAt:   now,
			ExpiresAt:   now.Add(time.Hour),
		}, cred.TokenHash))
		if desc == "revoke" {
			require.NoError(t, store.Revoke(context.Background(), cred.ID, time.Now().UTC()))
		}
	}

	// default: active only
	req := httptest.NewRequest(http.MethodGet, "/api/grants", nil)
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var got []Grant
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&got))
	require.Len(t, got, 1)
	require.Equal(t, "keep", got[0].Description)

	// all
	req = httptest.NewRequest(http.MethodGet, "/api/grants?status=all", nil)
	rr = httptest.NewRecorder()
	api.ServeHTTP(rr, req)
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&got))
	require.Len(t, got, 2)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/grants/... -run TestListGrants -v`
Expected: the handler returns 404 (unimplemented) or empty array.

**Step 3: Implement `handleList`**

Add to `api.go`:

```go
func (a *API) handleList(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("status") == "all"
	grants, err := a.store.List(r.Context(), all)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if grants == nil {
		grants = []Grant{} // stable JSON: [] not null
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(grants)
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/grants/... -v -race`
Expected: all PASS.

**Step 5: Commit**

```bash
git add mcp-broker/internal/grants/api.go mcp-broker/internal/grants/api_test.go
git commit -m "feat(grants): GET /api/grants"
```

---

### Task 14: `DELETE /api/grants/:id` handler

**Files:**

- Modify: `mcp-broker/internal/grants/api.go`
- Modify: `mcp-broker/internal/grants/api_test.go`

**Step 1: Write the failing test**

```go
func TestRevokeGrantEndpoint(t *testing.T) {
	store, _ := NewStore(context.Background(), openTestDB(t))
	engine := NewEngine(store)
	api := NewAPI(store, engine)

	cred, _ := NewCredential()
	now := time.Now().UTC()
	require.NoError(t, store.Create(context.Background(), Grant{
		ID:        cred.ID,
		Entries:   []Entry{{Tool: "x.y", ArgSchema: json.RawMessage(`{}`)}},
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}, cred.TokenHash))

	req := httptest.NewRequest(http.MethodDelete, "/api/grants/"+cred.ID, nil)
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)

	// idempotent — revoking again is also 204
	rr2 := httptest.NewRecorder()
	api.ServeHTTP(rr2, req)
	require.Equal(t, http.StatusNoContent, rr2.Code)

	g, _ := store.LookupByID(context.Background(), cred.ID)
	require.NotNil(t, g)
	require.NotNil(t, g.RevokedAt)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/grants/... -run TestRevokeGrant -v`
Expected: handler returns 404.

**Step 3: Implement `handleRevoke`**

Add to `api.go`:

```go
func (a *API) handleRevoke(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/grants/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "missing grant id", http.StatusBadRequest)
		return
	}
	if err := a.store.Revoke(r.Context(), id, time.Now().UTC()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.engine.Invalidate(id)
	w.WriteHeader(http.StatusNoContent)
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/grants/... -v -race`
Expected: all PASS.

**Step 5: Commit**

```bash
git add mcp-broker/internal/grants/api.go mcp-broker/internal/grants/api_test.go
git commit -m "feat(grants): DELETE /api/grants/:id"
```

---

### Task 15: Mount the API in serve.go

**Files:**

- Modify: `mcp-broker/cmd/mcp-broker/serve.go`

**Step 1: Locate the root mux**

The mux used for top-level routes is whatever `serve.go` sets up to aggregate `/mcp`, `/dashboard/`, etc. (see the line near 146 where the dashboard is mounted). The grants API is mounted at `/api/grants*` on the **same root mux**, wrapped with bearer auth (but NOT with the grant-token middleware, which only makes sense on the tool-call path).

**Step 2: Add the route**

Before `auth.Middleware(authToken, ...)` wraps the mux, add:

```go
grantsAPI := grants.NewAPI(grantStore, grantEngine)
rootMux.Handle("POST /api/grants", grantsAPI)
rootMux.Handle("GET /api/grants", grantsAPI)
rootMux.Handle("DELETE /api/grants/", grantsAPI)
```

(The ServeHTTP method above dispatches internally; handing the same handler to all three routes is fine.)

**Step 3: Smoke build and test**

Run (from `mcp-broker/`):

```bash
go build ./cmd/mcp-broker/
go test ./... -race
make test-e2e
```

Expected: all PASS.

**Step 4: Commit**

```bash
git add mcp-broker/cmd/mcp-broker/serve.go
git commit -m "feat(broker): mount /api/grants routes"
```

---

## Phase 4 — broker-cli

### Task 16: `broker-cli/internal/grants` client

**Files:**

- Create: `broker-cli/internal/grants/client.go`
- Test: `broker-cli/internal/grants/client_test.go`

**Step 1: Write the failing test**

```go
package grants

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClientCreate(t *testing.T) {
	var gotBody CreateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/grants", r.URL.Path)
		require.Equal(t, "Bearer s3cret", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(CreateResponse{
			ID:        "grt_test",
			Token:     "gr_test",
			Tools:     []string{"x.y"},
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().Add(time.Hour).UTC(),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "s3cret")
	resp, err := c.Create(context.Background(), CreateRequest{
		TTL:     Duration(time.Hour),
		Entries: []Entry{{Tool: "x.y", ArgSchema: json.RawMessage(`{}`)}},
	})
	require.NoError(t, err)
	require.Equal(t, "grt_test", resp.ID)
	require.Equal(t, "x.y", gotBody.Entries[0].Tool)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/grants/... -run TestClientCreate -v` (from `broker-cli/`)
Expected: `undefined: NewClient`.

**Step 3: Implement**

`broker-cli/internal/grants/client.go`:

```go
// Package grants is the broker-cli's thin HTTP client for the broker's
// /api/grants endpoints. Types mirror mcp-broker/internal/grants wire
// shapes but are redeclared here to avoid a cross-module dependency.
package grants

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

type Entry struct {
	Tool      string          `json:"tool"`
	ArgSchema json.RawMessage `json:"argSchema"`
}

type CreateRequest struct {
	Description string   `json:"description,omitempty"`
	TTL         Duration `json:"ttl"`
	Entries     []Entry  `json:"entries"`
}

type CreateResponse struct {
	ID          string    `json:"id"`
	Token       string    `json:"token"`
	Description string    `json:"description,omitempty"`
	Tools       []string  `json:"tools"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type Grant struct {
	ID          string    `json:"id"`
	Description string    `json:"description,omitempty"`
	Entries     []Entry   `json:"entries"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

func NewClient(endpoint, token string) *Client {
	return &Client{endpoint: endpoint, token: token, http: http.DefaultClient}
}

func (c *Client) Create(ctx context.Context, body CreateRequest) (*CreateResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/api/grants", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create grant: %s: %s", resp.Status, b)
	}
	var out CreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) List(ctx context.Context, includeInactive bool) ([]Grant, error) {
	u := c.endpoint + "/api/grants"
	if includeInactive {
		u += "?status=all"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list grants: %s: %s", resp.Status, b)
	}
	var out []Grant
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Revoke(ctx context.Context, id string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, c.endpoint+"/api/grants/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("revoke grant: %s: %s", resp.Status, b)
	}
	return nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/grants/... -v -race` (from `broker-cli/`)
Expected: PASS.

**Step 5: Commit**

```bash
git add broker-cli/internal/grants/client.go broker-cli/internal/grants/client_test.go
git commit -m "feat(broker-cli): HTTP client for grants API"
```

---

### Task 17: `--tool`-grouped flag pre-parser

**Files:**

- Create: `broker-cli/internal/grants/parse.go`
- Test: `broker-cli/internal/grants/parse_test.go`

**Step 1: Write the failing test**

```go
package grants

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitByToolBoundaries(t *testing.T) {
	args := []string{
		"--ttl", "1h",
		"--description", "push feat/foo",
		"--tool", "git.git_push",
		"--arg-equal", "branch=feat/foo",
		"--arg-equal", "force=false",
		"--tool", "git.git_fetch",
	}
	global, groups, err := splitByTool(args)
	require.NoError(t, err)
	require.Equal(t, []string{"--ttl", "1h", "--description", "push feat/foo"}, global)
	require.Len(t, groups, 2)
	require.Equal(t, "git.git_push", groups[0].tool)
	require.Equal(t, []string{"--arg-equal", "branch=feat/foo", "--arg-equal", "force=false"}, groups[0].flags)
	require.Equal(t, "git.git_fetch", groups[1].tool)
	require.Empty(t, groups[1].flags)
}

func TestSplitByToolNoTool(t *testing.T) {
	_, _, err := splitByTool([]string{"--ttl", "1h"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least one --tool")
}

func TestSplitByToolMissingName(t *testing.T) {
	_, _, err := splitByTool([]string{"--tool"})
	require.Error(t, err)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/grants/... -run TestSplitByTool -v`
Expected: `undefined: splitByTool`.

**Step 3: Implement**

`broker-cli/internal/grants/parse.go`:

```go
package grants

import "errors"

type toolGroup struct {
	tool  string
	flags []string
}

// splitByTool separates command-line args into global flags and tool-scoped
// flag groups. The first --tool delimits the boundary between globals and
// groups; every subsequent --tool opens a new group.
func splitByTool(args []string) ([]string, []toolGroup, error) {
	var (
		global []string
		groups []toolGroup
		cur    *toolGroup
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--tool" {
			if i+1 >= len(args) {
				return nil, nil, errors.New("--tool requires a name")
			}
			groups = append(groups, toolGroup{tool: args[i+1]})
			cur = &groups[len(groups)-1]
			i++
			continue
		}
		if cur == nil {
			global = append(global, a)
		} else {
			cur.flags = append(cur.flags, a)
		}
	}
	if len(groups) == 0 {
		return nil, nil, errors.New("at least one --tool is required")
	}
	return global, groups, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/grants/... -v -race`
Expected: PASS.

**Step 5: Commit**

```bash
git add broker-cli/internal/grants/parse.go broker-cli/internal/grants/parse_test.go
git commit -m "feat(broker-cli): split grant args by --tool boundaries"
```

---

### Task 18: Schema builder from per-arg flags

**Files:**

- Modify: `broker-cli/internal/grants/parse.go`
- Modify: `broker-cli/internal/grants/parse_test.go`

**Step 1: Write the failing test**

```go
func TestBuildSchema_AllOperators(t *testing.T) {
	group := toolGroup{
		tool: "git.git_push",
		flags: []string{
			"--arg-equal", "branch=feat/foo",
			"--arg-equal", "force=false",
			"--arg-match", "tag=^v[0-9]+$",
			"--arg-enum", "remote=origin,upstream",
		},
	}
	schema, err := buildSchema(group)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(schema, &decoded))

	require.Equal(t, "object", decoded["type"])
	props := decoded["properties"].(map[string]any)
	require.Equal(t, "feat/foo", props["branch"].(map[string]any)["const"])
	require.Equal(t, false, props["force"].(map[string]any)["const"])
	require.Equal(t, "^v[0-9]+$", props["tag"].(map[string]any)["pattern"])
	require.ElementsMatch(t, []any{"origin", "upstream"}, props["remote"].(map[string]any)["enum"])

	required := decoded["required"].([]any)
	require.ElementsMatch(t, []any{"branch", "force", "tag", "remote"}, required)
}

func TestBuildSchema_DotPathNesting(t *testing.T) {
	group := toolGroup{
		tool:  "x.y",
		flags: []string{"--arg-equal", "config.max_retries=3"},
	}
	schema, err := buildSchema(group)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(schema, &decoded))
	props := decoded["properties"].(map[string]any)
	config := props["config"].(map[string]any)
	require.Equal(t, "object", config["type"])
	nested := config["properties"].(map[string]any)
	require.EqualValues(t, 3, nested["max_retries"].(map[string]any)["const"])
}

func TestBuildSchema_SchemaFileExclusive(t *testing.T) {
	// --arg-schema-file plus any other --arg-* must error.
	group := toolGroup{
		tool: "x.y",
		flags: []string{
			"--arg-schema-file", "foo.json",
			"--arg-equal", "branch=main",
		},
	}
	_, err := buildSchema(group)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--arg-schema-file is mutually exclusive")
}
```

Add imports: `encoding/json`.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/grants/... -run TestBuildSchema -v`
Expected: `undefined: buildSchema`.

**Step 3: Implement**

Append to `parse.go`:

```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// buildSchema compiles one tool group's flags into a JSON Schema fragment.
// Returns the raw JSON bytes suitable for an Entry's ArgSchema.
func buildSchema(g toolGroup) (json.RawMessage, error) {
	if schemaFile := findSchemaFileFlag(g.flags); schemaFile != "" {
		if hasOtherArgFlags(g.flags) {
			return nil, errors.New("--arg-schema-file is mutually exclusive with other --arg-* flags")
		}
		return os.ReadFile(schemaFile)
	}

	root := map[string]any{"type": "object", "properties": map[string]any{}}
	required := []string{}

	for i := 0; i < len(g.flags); i++ {
		flag := g.flags[i]
		if i+1 >= len(g.flags) {
			return nil, fmt.Errorf("flag %s requires a value", flag)
		}
		val := g.flags[i+1]
		i++

		key, rawValue, err := parseKV(val)
		if err != nil {
			return nil, err
		}
		constraint, err := operatorToConstraint(flag, rawValue)
		if err != nil {
			return nil, err
		}

		// build nested path
		parent := root
		parts := strings.Split(key, ".")
		for depth, part := range parts {
			props := parent["properties"].(map[string]any)
			if depth == len(parts)-1 {
				props[part] = constraint
				required = appendUnique(required, part)
				break
			}
			child, ok := props[part].(map[string]any)
			if !ok {
				child = map[string]any{"type": "object", "properties": map[string]any{}}
				props[part] = child
			}
			// mark each intermediate required on its parent
			reqRaw, _ := parent["required"].([]any)
			already := false
			for _, v := range reqRaw {
				if v == part {
					already = true
					break
				}
			}
			if !already {
				parent["required"] = append(reqRaw, part)
			}
			parent = child
		}
	}

	if len(required) > 0 {
		// merge with any previously-seeded required slice on root
		existing, _ := root["required"].([]any)
		for _, r := range required {
			appendIfAbsent(&existing, r)
		}
		root["required"] = existing
	}
	return json.Marshal(root)
}

func parseKV(s string) (key string, rawValue string, err error) {
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return "", "", fmt.Errorf("expected key=value, got %q", s)
	}
	key = s[:eq]
	if strings.Contains(key, "..") || strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".") {
		return "", "", fmt.Errorf("invalid key %q", key)
	}
	return key, s[eq+1:], nil
}

func operatorToConstraint(flag, raw string) (map[string]any, error) {
	switch flag {
	case "--arg-equal":
		return map[string]any{"const": parseLiteral(raw)}, nil
	case "--arg-match":
		return map[string]any{"pattern": raw}, nil
	case "--arg-enum":
		parts := strings.Split(raw, ",")
		vals := make([]any, len(parts))
		for i, p := range parts {
			vals[i] = parseLiteral(p)
		}
		return map[string]any{"enum": vals}, nil
	case "--arg-schema-file":
		return nil, errors.New("--arg-schema-file must stand alone")
	default:
		return nil, fmt.Errorf("unknown flag %q", flag)
	}
}

func parseLiteral(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return s
}

func findSchemaFileFlag(flags []string) string {
	for i, f := range flags {
		if f == "--arg-schema-file" && i+1 < len(flags) {
			return flags[i+1]
		}
	}
	return ""
}

func hasOtherArgFlags(flags []string) bool {
	for _, f := range flags {
		if strings.HasPrefix(f, "--arg-") && f != "--arg-schema-file" {
			return true
		}
	}
	return false
}

func appendUnique(ss []string, s string) []string {
	for _, v := range ss {
		if v == s {
			return ss
		}
	}
	return append(ss, s)
}

func appendIfAbsent(dst *[]any, v string) {
	for _, s := range *dst {
		if s == v {
			return
		}
	}
	*dst = append(*dst, v)
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/grants/... -v -race`
Expected: PASS.

**Step 5: Commit**

```bash
git add broker-cli/internal/grants/parse.go broker-cli/internal/grants/parse_test.go
git commit -m "feat(broker-cli): build JSON Schema from --arg-* flags"
```

---

### Task 19: Pre-submit InputSchema validation

**Files:**

- Create: `broker-cli/internal/grants/validate.go`
- Test: `broker-cli/internal/grants/validate_test.go`

**Step 1: Write the failing test**

```go
package grants

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateAgainstInputSchema_UnknownArg(t *testing.T) {
	toolSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"branch": {"type": "string"}, "force": {"type": "boolean"}}
	}`)
	argSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"branc": {"const": "feat/foo"}},
		"required": ["branc"]
	}`)
	err := ValidateAgainstInputSchema(argSchema, toolSchema)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown arg")
	require.Contains(t, err.Error(), `did you mean "branch"`)
}

func TestValidateAgainstInputSchema_WrongType(t *testing.T) {
	toolSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"force": {"type": "boolean"}}
	}`)
	argSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"force": {"const": "feat/foo"}}
	}`)
	err := ValidateAgainstInputSchema(argSchema, toolSchema)
	require.Error(t, err)
	require.Contains(t, err.Error(), "type mismatch")
}

func TestValidateAgainstInputSchema_OK(t *testing.T) {
	toolSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"branch": {"type": "string"}, "force": {"type": "boolean"}}
	}`)
	argSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"branch": {"const": "feat/foo"}, "force": {"const": false}}
	}`)
	require.NoError(t, ValidateAgainstInputSchema(argSchema, toolSchema))
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/grants/... -run TestValidateAgainstInputSchema -v`
Expected: `undefined: ValidateAgainstInputSchema`.

**Step 3: Implement**

`broker-cli/internal/grants/validate.go`:

```go
package grants

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ValidateAgainstInputSchema checks that the keys referenced by argSchema
// exist in toolSchema's properties, and that any const values align with
// the declared types. It is a best-effort pre-submit check; the server
// re-compiles and re-validates regardless.
func ValidateAgainstInputSchema(argSchema, toolSchema json.RawMessage) error {
	var tool, arg map[string]any
	if err := json.Unmarshal(toolSchema, &tool); err != nil {
		return fmt.Errorf("parsing tool input schema: %w", err)
	}
	if err := json.Unmarshal(argSchema, &arg); err != nil {
		return fmt.Errorf("parsing arg schema: %w", err)
	}
	toolProps, _ := tool["properties"].(map[string]any)
	return walk(toolProps, arg)
}

func walk(toolProps map[string]any, argSchema map[string]any) error {
	argProps, _ := argSchema["properties"].(map[string]any)
	for key, raw := range argProps {
		toolEntry, ok := toolProps[key].(map[string]any)
		if !ok {
			return fmt.Errorf("unknown arg %q%s", key, suggestion(key, toolProps))
		}
		sub, _ := raw.(map[string]any)
		if cst, has := sub["const"]; has {
			if err := checkType(key, cst, toolEntry["type"]); err != nil {
				return err
			}
		}
		if enums, has := sub["enum"].([]any); has {
			for _, v := range enums {
				if err := checkType(key, v, toolEntry["type"]); err != nil {
					return err
				}
			}
		}
		if _, has := sub["properties"]; has {
			// nested object: recurse
			nestedProps, _ := toolEntry["properties"].(map[string]any)
			if err := walk(nestedProps, sub); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkType(key string, v any, declared any) error {
	want, _ := declared.(string)
	if want == "" {
		return nil
	}
	switch want {
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("arg %q: type mismatch (want string, got %T)", key, v)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("arg %q: type mismatch (want boolean, got %T)", key, v)
		}
	case "integer", "number":
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("arg %q: type mismatch (want %s, got %T)", key, want, v)
		}
	}
	return nil
}

func suggestion(bad string, toolProps map[string]any) string {
	best := ""
	bestDist := 1 << 30
	var keys []string
	for k := range toolProps {
		keys = append(keys, k)
	}
	sort.Strings(keys) // stable suggestion
	for _, k := range keys {
		d := levenshtein(bad, k)
		if d < bestDist {
			bestDist = d
			best = k
		}
	}
	if best != "" && bestDist <= 3 {
		return fmt.Sprintf(`; did you mean %q?`, best)
	}
	return ""
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	la, lb := len(a), len(b)
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// silence unused import warning in test file
var _ = strings.TrimSpace
```

> The `strings` import silencer at the bottom is a defensive stub; remove it if `strings` is used elsewhere in the file.

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/grants/... -v -race`
Expected: PASS.

**Step 5: Commit**

```bash
git add broker-cli/internal/grants/validate.go broker-cli/internal/grants/validate_test.go
git commit -m "feat(broker-cli): validate arg schema against tool InputSchema"
```

---

### Task 20: `broker-cli grant create` command

**Files:**

- Create: `broker-cli/cmd/broker-cli/grant.go`
- Modify: `broker-cli/cmd/broker-cli/root.go` (register the new command)

**Step 1: Build the command**

Create `broker-cli/cmd/broker-cli/grant.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	brokercli "<your-module>/broker-cli/internal/client"
	grantsclient "<your-module>/broker-cli/internal/grants"
)

func newGrantCmd(endpoint, authToken string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grant",
		Short: "Manage time-bounded authorization grants",
	}
	cmd.AddCommand(newGrantCreateCmd(endpoint, authToken))
	// list and revoke added in later tasks
	return cmd
}

func newGrantCreateCmd(endpoint, authToken string) *cobra.Command {
	var (
		ttl         time.Duration
		description string
	)
	cmd := &cobra.Command{
		Use:                "create",
		Short:              "Create a new grant (see --help for flag shape)",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := preParseGlobals(args, &ttl, &description); err != nil {
				return err
			}
			_, groups, err := splitByTool(stripGlobalFlags(args))
			if err != nil {
				return err
			}
			if ttl <= 0 {
				return fmt.Errorf("--ttl is required")
			}

			// fetch tool input schemas once for validation
			mcp := brokercli.New(endpoint, authToken)
			tools, err := mcp.ListTools(cmd.Context())
			if err != nil {
				return fmt.Errorf("listing tools: %w", err)
			}

			var entries []grantsclient.Entry
			for _, g := range groups {
				schema, err := buildSchema(g)
				if err != nil {
					return fmt.Errorf("tool %q: %w", g.tool, err)
				}
				toolSchema, ok := findToolInputSchema(tools, g.tool)
				if !ok {
					return fmt.Errorf("unknown tool %q", g.tool)
				}
				if err := grantsclient.ValidateAgainstInputSchema(schema, toolSchema); err != nil {
					return fmt.Errorf("tool %q: %w", g.tool, err)
				}
				entries = append(entries, grantsclient.Entry{Tool: g.tool, ArgSchema: schema})
			}

			resp, err := grantsclient.NewClient(endpoint, authToken).Create(cmd.Context(), grantsclient.CreateRequest{
				Description: description,
				TTL:         grantsclient.Duration(ttl),
				Entries:     entries,
			})
			if err != nil {
				return err
			}
			printGrantCreated(os.Stdout, resp)
			return nil
		},
	}
	return cmd
}

func printGrantCreated(w *os.File, r *grantsclient.CreateResponse) {
	fmt.Fprintf(w, "Grant created.\n")
	fmt.Fprintf(w, "  ID:          %s\n", r.ID)
	fmt.Fprintf(w, "  Token:       %s   ← copy now; will not be shown again\n", r.Token)
	fmt.Fprintf(w, "  Tools:       %s\n", strings.Join(r.Tools, ", "))
	ttl := r.ExpiresAt.Sub(r.CreatedAt).Round(time.Second)
	fmt.Fprintf(w, "  Expires:     %s (in %s)\n", r.ExpiresAt.Format(time.RFC3339), ttl)
	if r.Description != "" {
		fmt.Fprintf(w, "  Description: %s\n", r.Description)
	}
	fmt.Fprintf(w, "\nExport it for an agent session:\n  export MCP_BROKER_GRANT_TOKEN=%s\n", r.Token)
}
```

Add helpers `preParseGlobals`, `stripGlobalFlags`, and `findToolInputSchema` in the same file — these walk the raw `args` slice (which we've disabled Cobra from touching via `DisableFlagParsing: true`) and split out the `--ttl` and `--description` globals before calling `splitByTool`. Alternatively, lift the split logic into `broker-cli/internal/grants/parse.go` and test it there.

**Step 2: Register the command in `root.go`**

Inside `buildTree` (or wherever subcommands are added), after the existing tools tree is added:

```go
rootCmd.AddCommand(newGrantCmd(endpoint, token))
```

**Step 3: End-to-end smoke test**

Run the broker locally in one shell:

```bash
cd mcp-broker && go run ./cmd/mcp-broker serve
```

In another shell, with `MCP_BROKER_ENDPOINT` and `MCP_BROKER_AUTH_TOKEN` set:

```bash
cd broker-cli
go run ./cmd/broker-cli grant create --ttl 1h \
  --tool git.git_push --arg-equal branch=feat/foo --arg-equal force=false
```

Expected: prints a `grt_…` ID and a `gr_…` token.

**Step 4: Commit**

```bash
git add broker-cli/cmd/broker-cli/grant.go broker-cli/cmd/broker-cli/root.go broker-cli/internal/grants/
git commit -m "feat(broker-cli): grant create subcommand"
```

---

### Task 21: `grant list` and `grant revoke`

**Files:**

- Modify: `broker-cli/cmd/broker-cli/grant.go`

**Step 1: Add the `list` command**

Append:

```go
func newGrantListCmd(endpoint, authToken string) *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List grants",
		RunE: func(cmd *cobra.Command, args []string) error {
			grants, err := grantsclient.NewClient(endpoint, authToken).List(cmd.Context(), all)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(grants)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTOOLS\tEXPIRES\tSTATUS\tDESCRIPTION")
			for _, g := range grants {
				tools := make([]string, len(g.Entries))
				for i, e := range g.Entries {
					tools[i] = e.Tool
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					g.ID, strings.Join(tools, ","),
					g.ExpiresAt.Format(time.RFC3339),
					statusOf(&g), g.Description)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include expired and revoked grants")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON output")
	return cmd
}

func statusOf(g *grantsclient.Grant) string {
	if g.RevokedAt != nil {
		return "revoked"
	}
	if time.Now().After(g.ExpiresAt) {
		return "expired"
	}
	return "active"
}
```

Add imports: `text/tabwriter`.

**Step 2: Add the `revoke` command**

```go
func newGrantRevokeCmd(endpoint, authToken string) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <grant-id>",
		Short: "Revoke a grant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := grantsclient.NewClient(endpoint, authToken).Revoke(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "revoked %s\n", args[0])
			return nil
		},
	}
}
```

**Step 3: Register them** in `newGrantCmd`:

```go
cmd.AddCommand(newGrantListCmd(endpoint, authToken))
cmd.AddCommand(newGrantRevokeCmd(endpoint, authToken))
```

**Step 4: Smoke test**

Against a running broker:

```bash
broker-cli grant list
broker-cli grant list --all
broker-cli grant revoke grt_xxxxxxxx
broker-cli grant list --all   # shows revoked row
```

**Step 5: Commit**

```bash
git add broker-cli/cmd/broker-cli/grant.go
git commit -m "feat(broker-cli): grant list and revoke subcommands"
```

---

## Phase 5 — Dashboard

### Task 22: Grants tab HTML + fetch

**Files:**

- Modify: `mcp-broker/internal/dashboard/index.html`
- Modify: `mcp-broker/internal/dashboard/dashboard.go` (add `handleGrants`)

**Step 1: Add a `/api/grants` proxy handler on the dashboard mux**

In `dashboard.go`, next to the existing `mux.HandleFunc("GET /api/rules", ...)` block, add:

```go
mux.HandleFunc("GET /api/grants", d.handleGrants)
```

And the handler:

```go
func (d *Dashboard) handleGrants(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("status") == "all"
	grants, err := d.grantStore.List(r.Context(), all)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if grants == nil {
		grants = []grants.Grant{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(grants)
}
```

Add `grantStore *grants.Store` to the `Dashboard` struct and accept it in the dashboard constructor. Thread it from `serve.go`.

**Step 2: Add the Grants tab to `index.html`**

Find the tab bar (line ~142 per survey). Add a `<button>` for "Grants" matching the existing tab-button structure. Add a `<section id="grants">` with a `<table>` and a client-side fetch that populates rows.

```html
<section id="tab-grants" class="tab hidden">
  <h2>Grants</h2>
  <div class="controls">
    <label
      ><input type="checkbox" id="grants-include-inactive" /> Include expired /
      revoked</label
    >
  </div>
  <table id="grants-table">
    <thead>
      <tr>
        <th>ID</th>
        <th>Description</th>
        <th>Tools</th>
        <th>Expires</th>
        <th>Status</th>
      </tr>
    </thead>
    <tbody></tbody>
  </table>
</section>

<script>
  async function loadGrants() {
    const all = document.getElementById("grants-include-inactive").checked;
    const r = await fetch(`/dashboard/api/grants${all ? "?status=all" : ""}`);
    const rows = await r.json();
    const tbody = document.querySelector("#grants-table tbody");
    tbody.innerHTML = "";
    for (const g of rows) {
      const tr = document.createElement("tr");
      const status = g.revoked_at
        ? "revoked"
        : new Date(g.expires_at) < new Date()
          ? "expired"
          : "active";
      const tools = g.entries.map((e) => e.tool).join(", ");
      tr.innerHTML = `<td>${g.id}</td><td>${g.description ?? ""}</td><td>${tools}</td><td>${g.expires_at}</td><td class="status-${status}">${status}</td>`;
      tbody.appendChild(tr);
    }
  }
  document
    .getElementById("grants-include-inactive")
    .addEventListener("change", loadGrants);
  // wire up with the existing tab-switching logic so loadGrants() runs when the Grants tab is activated
</script>
```

Follow the existing client-side JS style in `index.html` for tab activation hooks. Add CSS for `.status-active`, `.status-expired`, `.status-revoked` chips.

**Step 3: Manual smoke test**

Run the broker, open `/dashboard/`, click the new Grants tab, confirm rows render.

**Step 4: Commit**

```bash
git add mcp-broker/internal/dashboard/
git commit -m "feat(dashboard): add read-only Grants tab"
```

---

### Task 23: Audit tab — grant pill and filter

**Files:**

- Modify: `mcp-broker/internal/dashboard/dashboard.go` (ensure `/api/audit` returns the new columns)
- Modify: `mcp-broker/internal/dashboard/index.html` (render the pill, add filter)

**Step 1: Confirm `/api/audit` returns `grant_id` and `grant_outcome`**

Since Task 7 extended the `Record` struct with JSON tags, the existing `handleAudit` encode-to-JSON should already pass the new fields through. Quick verification:

```bash
# with broker running
curl -s -H "Authorization: Bearer $MCP_BROKER_AUTH_TOKEN" http://localhost:8080/dashboard/api/audit | jq '.[0] | keys'
```

Expected output includes `"grant_id"` and `"grant_outcome"`.

If the dashboard uses a view-model struct distinct from `audit.Record`, add the fields there too.

**Step 2: Update the audit tab's row rendering**

In the JS that builds each audit row, add:

```javascript
if (record.grant_id) {
  const pill = document.createElement("span");
  pill.className = `grant-pill grant-${record.grant_outcome}`;
  pill.textContent = `🎫 ${record.grant_id} (${record.grant_outcome})`;
  row.querySelector(".tool-cell").appendChild(pill);
}
```

Add a filter chip near the existing audit filters:

```html
<label
  ><input type="checkbox" id="audit-has-grant" /> Only rows with a grant</label
>
```

And a search box for grant id. The filter logic runs client-side over the fetched records (or forwards as a query param if `/api/audit` supports filters — check the existing code).

**Step 3: Manual smoke test**

Create a grant, make a tool call that matches, make a call that falls through, make a call with a bogus token. Confirm the Audit tab shows three rows with the correct pill styles.

**Step 4: Commit**

```bash
git add mcp-broker/internal/dashboard/
git commit -m "feat(dashboard): show grant pill in audit rows"
```

---

## Phase 6 — Documentation

### Task 24: Update CLAUDE.md files

**Files:**

- Modify: `mcp-broker/CLAUDE.md`
- Modify: `broker-cli/CLAUDE.md`

**Step 1: Add grants to the mcp-broker architecture block**

Under the `internal/` package listing in `mcp-broker/CLAUDE.md`, add a line between `auth/` and `broker/`:

```
  grants/               SQLite-backed time-bounded authorizations; complement to rules engine
```

Add a new bullet to the Conventions list:

```
- Grant bearer tokens follow the same pattern as the auth token: 32 random bytes, hex-encoded, only SHA-256(token) persisted in SQLite
- Grants are additive only — a presented grant can allow a call but never blocks one; invalid/mismatched grants fall through to the rules engine
```

And to the Architecture pipeline description:

```
Pipeline: tool call → grant check → rules check → optional approval → proxy to backend → audit.
```

**Step 2: Add grants to the broker-cli architecture block**

Under `internal/` in `broker-cli/CLAUDE.md`, add:

```
  grants/            HTTP client and CLI grammar for the broker's /api/grants endpoints
```

Add a Conventions bullet:

```
- Grant create flags follow a --tool-grouped shape: every --arg-* flag attaches to the most recent --tool; --arg-schema-file is mutually exclusive with other --arg-* flags for the same tool
```

**Step 3: Run `make audit` once in each module and commit**

```bash
(cd mcp-broker && make audit)
(cd broker-cli && make audit)
git add mcp-broker/CLAUDE.md broker-cli/CLAUDE.md
git commit -m "docs: document grants in CLAUDE.md files"
```

---

## Verification checklist before shipping

- `make audit` passes in both `mcp-broker/` and `broker-cli/`
- `make test-e2e` passes in `mcp-broker/`
- Manual flow: create grant → agent call matches → audit shows `matched` → revoke → next call shows `invalid`
- Dashboard Grants tab lists active grants; Audit tab shows pills
- Old audit DBs on disk migrate cleanly (start the broker against a pre-change DB; new columns appear via `ALTER TABLE`)
- `broker-cli grant create` with `--arg-schema-file` AND any other `--arg-*` errors out
- Presenting a bogus `X-Grant-Token` logs `grant_outcome=invalid` but does not deny the request
