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
