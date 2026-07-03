package rules

import (
	"sync/atomic"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// Evaluation is the result of evaluating one immutable rules snapshot.
type Evaluation struct {
	Verdict    Verdict
	RuleReason string
	Matched    bool
}

// Store holds the active rules engine and atomically swaps complete snapshots.
type Store struct {
	engine atomic.Pointer[Engine]
}

// NewStore creates a reloadable rules store.
func NewStore(rs []config.RuleConfig) (*Store, error) {
	engine, err := New(rs)
	if err != nil {
		return nil, err
	}
	store := &Store{}
	store.engine.Store(engine)
	return store, nil
}

// Reload compiles and atomically swaps a complete rules snapshot.
func (s *Store) Reload(rs []config.RuleConfig) error {
	engine, err := New(rs)
	if err != nil {
		return err
	}
	s.engine.Store(engine)
	return nil
}

// EvaluateWithMetadata evaluates a tool call against one active rules snapshot
// and returns any matched rule metadata from that same snapshot.
func (s *Store) EvaluateWithMetadata(tool string, args map[string]any) Evaluation {
	return s.engine.Load().EvaluateWithMetadata(tool, args)
}

// Rules returns a copy of the active rules in evaluation order.
func (s *Store) Rules() []config.RuleConfig {
	return s.engine.Load().Rules()
}
