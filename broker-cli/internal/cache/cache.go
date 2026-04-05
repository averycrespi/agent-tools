package cache

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
)

// Cache stores the tool list in a temp file with a TTL.
type Cache struct {
	ttl time.Duration
}

// New creates a Cache with the given TTL.
func New(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl}
}

type entry struct {
	Tools     []client.Tool `json:"tools"`
	ExpiresAt time.Time     `json:"expires_at"`
}

// Get returns cached tools for the given endpoint, if still valid.
func (c *Cache) Get(endpoint string) ([]client.Tool, bool) {
	data, err := os.ReadFile(c.path(endpoint))
	if err != nil {
		return nil, false
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, false
	}
	if time.Now().After(e.ExpiresAt) {
		return nil, false
	}
	return e.Tools, true
}

// Set writes the tool list to the cache for the given endpoint.
func (c *Cache) Set(endpoint string, tools []client.Tool) error {
	e := entry{Tools: tools, ExpiresAt: time.Now().Add(c.ttl)}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}
	if err := os.WriteFile(c.path(endpoint), data, 0o600); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	return nil
}

func (c *Cache) path(endpoint string) string {
	h := sha256.Sum256([]byte(endpoint))
	name := fmt.Sprintf("broker-cli-tools-%x.json", h[:8])
	return filepath.Join(os.TempDir(), name)
}
