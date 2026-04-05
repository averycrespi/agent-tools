package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fakeTelegramServer(t *testing.T, callbackData string) (*httptest.Server, *int32) {
	t.Helper()
	var messageID int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			atomic.StoreInt32(&messageID, 42)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"message_id": 42},
			})
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			id := int(atomic.LoadInt32(&messageID))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": []map[string]any{{
					"update_id": 1,
					"callback_query": map[string]any{
						"id":   "cq1",
						"data": callbackData,
						"message": map[string]any{
							"message_id": id,
						},
					},
				}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	return srv, &messageID
}

func TestApprover_Review_Approves(t *testing.T) {
	srv, _ := fakeTelegramServer(t, "approve")
	defer srv.Close()

	a := newWithBase("token", "123", srv.URL, &http.Client{Timeout: 5 * time.Second}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	approved, reason, err := a.Review(ctx, "github.push", map[string]any{"branch": "main"})
	require.NoError(t, err)
	require.True(t, approved)
	require.Empty(t, reason)
}

func TestApprover_Review_Denies(t *testing.T) {
	srv, _ := fakeTelegramServer(t, "deny")
	defer srv.Close()

	a := newWithBase("token", "123", srv.URL, &http.Client{Timeout: 5 * time.Second}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	approved, reason, err := a.Review(ctx, "github.push", nil)
	require.NoError(t, err)
	require.False(t, approved)
	require.Equal(t, "user", reason)
}

func TestApprover_Review_ContextCancelled(t *testing.T) {
	// Fake server that never returns a callback
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"message_id": 1},
			})
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			// Hold until request context is done
			<-r.Context().Done()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	defer srv.Close()

	a := newWithBase("token", "123", srv.URL, &http.Client{Timeout: 5 * time.Second}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	approved, reason, err := a.Review(ctx, "test.tool", nil)
	require.NoError(t, err) // context cancel is not returned as an error
	require.False(t, approved)
	require.Equal(t, "timeout", reason)
}

func TestFormatArgs_TruncatesLongJSON(t *testing.T) {
	args := map[string]any{"key": strings.Repeat("x", 300)}
	result := formatArgs(args)
	require.LessOrEqual(t, len([]rune(result)), maxArgLen+len("... (truncated)"))
	require.Contains(t, result, "(truncated)")
}

func TestFormatArgs_EmptyArgs(t *testing.T) {
	require.Equal(t, "(no args)", formatArgs(nil))
	require.Equal(t, "(no args)", formatArgs(map[string]any{}))
}
