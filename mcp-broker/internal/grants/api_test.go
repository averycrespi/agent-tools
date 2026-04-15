package grants

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCreateGrantEndpoint(t *testing.T) {
	store, err := NewStore(context.Background(), openTestDB(t))
	require.NoError(t, err)
	api := NewAPI(store, NewEngine(store))

	body := CreateRequest{
		Description: "push feat/foo",
		TTL:         Duration(time.Hour),
		Entries: []Entry{
			{Tool: "git.git_push", ArgSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/grants", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)

	var resp CreateResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.NotEmpty(t, resp.ID)
	require.NotEmpty(t, resp.Token, "raw token must be returned exactly once")
	require.NotZero(t, resp.ExpiresAt)

	g, err := store.LookupByTokenHash(context.Background(), HashToken(resp.Token))
	require.NoError(t, err)
	require.NotNil(t, g)
	require.Equal(t, resp.ID, g.ID)
}

func TestCreateGrantRejectsBadSchema(t *testing.T) {
	store, _ := NewStore(context.Background(), openTestDB(t))
	api := NewAPI(store, NewEngine(store))

	body := CreateRequest{
		TTL: Duration(time.Hour),
		Entries: []Entry{
			{Tool: "git.git_push", ArgSchema: json.RawMessage(`{"type": 123}`)}, // invalid
		},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/grants", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
}
