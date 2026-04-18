package public

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetcher_ForwardsToUpstream(t *testing.T) {
	var gotPath string
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Version":"v1.0.0"}`)
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	f := New(u)

	req := httptest.NewRequest(http.MethodGet, "/rsc.io/quote/@v/list", nil)
	req.Header.Set("Authorization", "Bearer super-secret")
	w := httptest.NewRecorder()
	f.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/rsc.io/quote/@v/list", gotPath)
	assert.Empty(t, gotAuth, "upstream must not see the inbound token")
	assert.Contains(t, w.Body.String(), "v1.0.0")
}
