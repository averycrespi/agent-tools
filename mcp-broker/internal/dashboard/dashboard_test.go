package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDashboard_Review_ApprovesViaAPI(t *testing.T) {
	d := New(nil, nil, nil)
	mux := d.Handler()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Start a review in a goroutine
	done := make(chan bool, 1)
	go func() {
		approved, _, err := d.Review(context.Background(), "github.push", map[string]any{"branch": "main"})
		require.NoError(t, err)
		done <- approved
	}()

	// Wait for the pending request to appear
	time.Sleep(50 * time.Millisecond)

	// Get pending requests
	resp, err := http.Get(srv.URL + "/api/pending")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var pending []pendingRequest
	err = json.NewDecoder(resp.Body).Decode(&pending)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	// Approve it
	body := `{"id":"` + pending[0].ID + `","decision":"approve"}`
	resp2, err := http.Post(srv.URL+"/api/decide", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	approved := <-done
	require.True(t, approved)
}

func TestDashboard_Review_DeniesViaAPI(t *testing.T) {
	d := New(nil, nil, nil)
	mux := d.Handler()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	type result struct {
		approved bool
		reason   string
	}
	done := make(chan result, 1)
	go func() {
		approved, reason, err := d.Review(context.Background(), "github.push", map[string]any{})
		require.NoError(t, err)
		done <- result{approved, reason}
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(srv.URL + "/api/pending")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var pending []pendingRequest
	err = json.NewDecoder(resp.Body).Decode(&pending)
	require.NoError(t, err)

	body := `{"id":"` + pending[0].ID + `","decision":"deny"}`
	resp2, err := http.Post(srv.URL+"/api/decide", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()

	r := <-done
	require.False(t, r.approved)
	require.Equal(t, "user", r.reason)
}

func TestDashboard_Review_CancelsOnContextDone(t *testing.T) {
	d := New(nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := d.Review(ctx, "test.tool", nil)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	err := <-done
	require.Error(t, err)
}

func TestDashboard_UnauthorizedPage(t *testing.T) {
	d := New(nil, nil, nil)
	mux := d.Handler()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/unauthorized")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "Unauthorized")
}
