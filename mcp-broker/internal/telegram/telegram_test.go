package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestApprover_Review_ConcurrentCallbacksAreDispatchedByMessageID(t *testing.T) {
	var nextMessageID int32 = 100
	var sentMessages int32
	var updatesSent atomic.Bool
	allMessagesSent := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			id := atomic.AddInt32(&nextMessageID, 1)
			if atomic.AddInt32(&sentMessages, 1) == 2 {
				close(allMessagesSent)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"message_id": id},
			})
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			<-allMessagesSent
			if updatesSent.CompareAndSwap(false, true) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok": true,
					"result": []map[string]any{
						{
							"update_id": 1,
							"callback_query": map[string]any{
								"id":   "cq101",
								"data": "approve",
								"message": map[string]any{
									"message_id": 101,
								},
							},
						},
						{
							"update_id": 2,
							"callback_query": map[string]any{
								"id":   "cq102",
								"data": "approve",
								"message": map[string]any{
									"message_id": 102,
								},
							},
						},
					},
				})
				return
			}
			<-r.Context().Done()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	defer srv.Close()

	a := newWithBase("token", "123", srv.URL, &http.Client{Timeout: 5 * time.Second}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	results := make(chan bool, 2)
	reasons := make(chan string, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			approved, reason, err := a.Review(ctx, "github.push", nil)
			results <- approved
			reasons <- reason
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(reasons)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	for reason := range reasons {
		require.Empty(t, reason)
	}
	for approved := range results {
		require.True(t, approved)
	}
}

type fakeToolLister map[string]string

func (f fakeToolLister) ToolDescription(name string) string {
	return f[name]
}

func TestApprover_Review_EscapesHTMLInApprovalMessage(t *testing.T) {
	messageText := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var req sendMessageReq
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			messageText <- req.Text
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"message_id": 1},
			})
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": []map[string]any{seqCallbackUpdate(1, 1, "cq1", "approve")},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	defer srv.Close()

	a := newWithBase("token", "123", srv.URL, &http.Client{Timeout: 5 * time.Second}, nil)
	a.WithTools(fakeToolLister{"evil</code>": "desc & <b>spoof</b>"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	approved, reason, err := a.Review(ctx, "evil</code>", map[string]any{
		"body": "</pre><b>approved</b><pre>&",
	})

	require.NoError(t, err)
	require.True(t, approved)
	require.Empty(t, reason)
	text := <-messageText
	require.Contains(t, text, "<code>evil&lt;/code&gt;</code>")
	require.Contains(t, text, "desc &amp; &lt;b&gt;spoof&lt;/b&gt;")
	require.Contains(t, text, "&lt;/pre&gt;&lt;b&gt;approved&lt;/b&gt;&lt;pre&gt;&amp;")
	require.NotContains(t, text, "evil</code>")
	require.NotContains(t, text, "</pre><b>approved</b><pre>")
}

func TestResolvedText_EscapesHTMLInDetail(t *testing.T) {
	text := resolvedText(true, "", nil, context.Background(), "evil</code>", `{"body":"</pre><b>approved</b><pre>&"}`)

	require.Contains(t, text, "<code>evil&lt;/code&gt;</code>")
	require.Contains(t, text, "&lt;/pre&gt;&lt;b&gt;approved&lt;/b&gt;&lt;pre&gt;&amp;")
	require.NotContains(t, text, "evil</code>")
	require.NotContains(t, text, "</pre><b>approved</b><pre>")
}

func seqCallbackUpdate(updateID, messageID int, callbackID, data string) map[string]any {
	return map[string]any{
		"update_id": updateID,
		"callback_query": map[string]any{
			"id":   callbackID,
			"data": data,
			"message": map[string]any{
				"message_id": messageID,
			},
		},
	}
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

func TestFormatArgs_TruncatesPerValue(t *testing.T) {
	args := map[string]any{
		"long":  strings.Repeat("x", 300),
		"short": "hello",
	}
	result := formatArgs(args)
	require.Contains(t, result, "(truncated)")
	require.Contains(t, result, `"short": "hello"`)
}

func TestFormatArgs_PreservesShortValues(t *testing.T) {
	args := map[string]any{"key": "value"}
	result := formatArgs(args)
	require.NotContains(t, result, "truncated")
	require.Contains(t, result, `"key": "value"`)
}

func TestFormatArgs_EmptyArgs(t *testing.T) {
	require.Equal(t, "(no args)", formatArgs(nil))
	require.Equal(t, "(no args)", formatArgs(map[string]any{}))
}
