//go:build e2e

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDemoClientFailureProblemPrivacy(t *testing.T) {
	const canary = "private-response-canary"
	const base = "PATCH /api/v2/principals/fixture returned HTTP 503"
	for _, tt := range []struct {
		name, body, want string
	}{
		{"known", `{"status":503,"code":"authorization_unavailable","title":"` + canary + `","detail":"` + canary + `"}`, base + " (problem=authorization_unavailable)"},
		{"unknown", `{"code":"` + canary + `"}`, base},
		{"mismatched status", `{"code":"not_found","title":"` + canary + `"}`, base},
		{"malformed", `{"code":"authorization_unavailable","detail":"` + canary, base},
		{"duplicate", `{"code":"authorization_unavailable","code":"` + canary + `"}`, base},
		{"trailing", `{"code":"authorization_unavailable"}` + canary, base},
		{"wrong type", `{"code":{"secret":"` + canary + `"}}`, base},
		{"missing", `{"detail":"` + canary + `"}`, base},
		{"deep", `{"code":"authorization_unavailable","detail":[[[[["` + canary + `"]]]]]}`, base},
		{"diagnostic bound", `{"code":"authorization_unavailable","detail":"` + strings.Repeat(canary, 256) + `"}`, base},
		{"response bound", strings.Repeat(canary, outputLimit/len(canary)+1), "public response exceeded bound or failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			c := newClient(t.Context(), strings.TrimPrefix(server.URL, "http://"), "request-bearer-canary")
			result, headers := c.request("PATCH", "/api/v2/principals/fixture?secret=query-canary", object{"state": "disabled"}, nil, http.StatusOK, "")
			require.Nil(t, result)
			require.Nil(t, headers)
			require.EqualError(t, c.err, tt.want)
			c.request("PATCH", "/api/v2/principals/fixture", nil, nil, http.StatusOK, "")
			require.EqualError(t, c.err, tt.want)
			require.EqualValues(t, 1, requests.Load())
		})
	}
}

func TestDemoClientSuccessUnchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", "fixture-revision")
		_, _ = io.WriteString(w, `{"state":"disabled"}`)
	}))
	defer server.Close()
	c := newClient(t.Context(), strings.TrimPrefix(server.URL, "http://"), "fixture-bearer")
	result, headers := c.request("PATCH", "/api/v2/principals/fixture", object{"state": "disabled"}, nil, http.StatusOK, "")
	require.NoError(t, c.err)
	require.Equal(t, object{"state": "disabled"}, result)
	require.Equal(t, "fixture-revision", headers.Get("ETag"))
}
