package cache_test

import (
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/broker-cli/internal/cache"
	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tools() []client.Tool {
	return []client.Tool{
		{Name: "git.push", Description: "Push commits", InputSchema: map[string]any{}},
	}
}

func TestCache_missOnEmpty(t *testing.T) {
	c := cache.New(30 * time.Second)
	got, ok := c.Get("http://localhost:8200")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestCache_hitAfterSet(t *testing.T) {
	c := cache.New(30 * time.Second)
	require.NoError(t, c.Set("http://localhost:8200", tools()))
	got, ok := c.Get("http://localhost:8200")
	assert.True(t, ok)
	assert.Equal(t, tools(), got)
}

func TestCache_missAfterExpiry(t *testing.T) {
	c := cache.New(10 * time.Millisecond)
	require.NoError(t, c.Set("http://localhost:8200", tools()))
	time.Sleep(20 * time.Millisecond)
	_, ok := c.Get("http://localhost:8200")
	assert.False(t, ok)
}

func TestCache_differentKeys(t *testing.T) {
	c := cache.New(30 * time.Second)
	require.NoError(t, c.Set("http://localhost:8200", tools()))
	_, ok := c.Get("http://localhost:9999")
	assert.False(t, ok)
}
