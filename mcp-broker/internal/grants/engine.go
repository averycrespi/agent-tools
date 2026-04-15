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
