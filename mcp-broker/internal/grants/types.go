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
	ID          string     `json:"id"`
	Description string     `json:"description,omitempty"`
	Entries     []Entry    `json:"entries"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
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
