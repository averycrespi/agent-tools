package rules

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

func TestStoreReloadSwapsRulesAfterSuccessfulCompile(t *testing.T) {
	store, err := NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny", Reason: "initial"}})
	require.NoError(t, err)

	result := store.EvaluateWithMetadata("github.search", nil)
	require.Equal(t, Deny, result.Verdict)
	require.True(t, result.Matched)
	require.Equal(t, "initial", result.RuleReason)

	err = store.Reload([]config.RuleConfig{{Tool: "github.*", Verdict: "allow", Reason: "reloaded"}})
	require.NoError(t, err)

	result = store.EvaluateWithMetadata("github.search", nil)
	require.Equal(t, Allow, result.Verdict)
	require.True(t, result.Matched)
	require.Equal(t, "reloaded", result.RuleReason)
	require.Equal(t, []config.RuleConfig{{Tool: "github.*", Verdict: "allow", Reason: "reloaded"}}, store.Rules())
}

func TestStoreReloadFailureLeavesPreviousRulesActive(t *testing.T) {
	store, err := NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny", Reason: "old"}})
	require.NoError(t, err)

	err = store.Reload([]config.RuleConfig{{
		Tool:    "*",
		Verdict: "allow",
		Args: []config.ArgPattern{
			{Path: "bad..path", Match: json.RawMessage(`"value"`)},
		},
	}})
	require.Error(t, err)

	result := store.EvaluateWithMetadata("anything", nil)
	require.Equal(t, Deny, result.Verdict)
	require.True(t, result.Matched)
	require.Equal(t, "old", result.RuleReason)
}

func TestStoreReloadInvalidRegexLeavesPreviousRulesActive(t *testing.T) {
	store, err := NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny", Reason: "old"}})
	require.NoError(t, err)

	err = store.Reload([]config.RuleConfig{{
		Tool:    "*",
		Verdict: "allow",
		Args: []config.ArgPattern{
			{Path: "branch", Match: json.RawMessage(`{"regex": "[invalid"}`)},
		},
	}})
	require.Error(t, err)

	result := store.EvaluateWithMetadata("anything", nil)
	require.Equal(t, Deny, result.Verdict)
	require.True(t, result.Matched)
	require.Equal(t, "old", result.RuleReason)
}

func TestStoreRulesReturnsDeepCopy(t *testing.T) {
	store, err := NewStore([]config.RuleConfig{{
		Tool:    "*",
		Verdict: "deny",
		Args: []config.ArgPattern{
			{Path: "branch", Match: json.RawMessage(`"main"`)},
		},
	}})
	require.NoError(t, err)

	got := store.Rules()
	got[0].Tool = "mutated"
	got[0].Args[0].Path = "mutated"
	got[0].Args[0].Match[0] = '{'

	again := store.Rules()
	require.Equal(t, "*", again[0].Tool)
	require.Equal(t, "branch", again[0].Args[0].Path)
	require.JSONEq(t, `"main"`, string(again[0].Args[0].Match))
}

func TestStoreConcurrentReloadAndEvaluate(t *testing.T) {
	store, err := NewStore([]config.RuleConfig{{Tool: "target", Verdict: "deny", Reason: "a"}})
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if i%2 == 0 {
				if err := store.Reload([]config.RuleConfig{{Tool: "target", Verdict: "deny", Reason: "a"}}); err != nil {
					t.Errorf("reload a: %v", err)
				}
				continue
			}
			if err := store.Reload([]config.RuleConfig{
				{Tool: "other", Verdict: "allow"},
				{Tool: "target", Verdict: "deny", Reason: "b"},
			}); err != nil {
				t.Errorf("reload b: %v", err)
			}
		}
	}()

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				result := store.EvaluateWithMetadata("target", nil)
				if result.Verdict != Deny || !result.Matched || (result.RuleReason != "a" && result.RuleReason != "b") {
					t.Errorf("inconsistent evaluation: verdict=%s matched=%v reason=%q", result.Verdict, result.Matched, result.RuleReason)
				}
			}
		}()
	}

	wg.Wait()
}

func TestStoreEvaluateWithMetadataReturnsReasonValue(t *testing.T) {
	store, err := NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny", Reason: "stable"}})
	require.NoError(t, err)

	result := store.EvaluateWithMetadata("anything", nil)
	require.Equal(t, Deny, result.Verdict)
	require.True(t, result.Matched)
	result.RuleReason = "mutated"

	again := store.EvaluateWithMetadata("anything", nil)
	require.Equal(t, "stable", again.RuleReason)
}
