package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFailureDiagnosticsAreTypedAndOneShot(t *testing.T) {
	const canary = "SECRET-HEADER-BODY-ARGUMENT"
	for _, tc := range []struct {
		status                int
		body, category, phase string
	}{
		{401, canary, "authentication", "response_status"}, {429, canary, "rate_limit", "response_status"},
		{200, canary, "json_decode", "response_decode"}, {200, `{"secret":"` + canary + `"}`, "response_contract", "response_validation"},
	} {
		t.Run(tc.category, func(t *testing.T) {
			client, attempts := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "15")
				w.Header().Set("X-Secret", canary)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := client.Call(t.Context(), "evaluate", decode(t, strings.Replace(mixed, "hello", canary, 1)))
			require.Error(t, err)
			diagnostic, ok := Diagnostic(err)
			require.True(t, ok)
			require.Equal(t, tc.category, diagnostic.Category)
			require.Equal(t, tc.phase, diagnostic.Phase)
			if tc.status != 200 {
				require.Equal(t, tc.status, *diagnostic.HTTPStatus)
				require.Equal(t, 15, *diagnostic.RetryAfterSeconds)
			}
			raw, e := json.Marshal(diagnostic)
			require.NoError(t, e)
			require.NotContains(t, string(raw)+err.Error(), canary)
			require.EqualValues(t, 1, attempts.Load())
		})
	}
	for _, tc := range []struct {
		err      error
		category string
	}{{context.DeadlineExceeded, "timeout"}, {context.Canceled, "canceled"}, {errors.New(canary), "transport"}} {
		calls := 0
		client := newClient(canary, roundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, tc.err }))
		_, err := client.Call(t.Context(), "list_models", object{})
		d, ok := Diagnostic(err)
		require.True(t, ok)
		require.Equal(t, tc.category, d.Category)
		require.Equal(t, 1, calls)
		require.NotContains(t, err.Error(), canary)
	}
}

func TestFailureRetryGuidanceBounds(t *testing.T) {
	for _, value := range []string{"secret", "-1", "86401", "4294967296", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), time.Now().Add(48 * time.Hour).UTC().Format(http.TimeFormat)} {
		response := &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{value}}}
		d, ok := Diagnostic(statusError(response))
		require.True(t, ok)
		require.Nil(t, d.RetryAfterSeconds)
	}
	d, ok := Diagnostic(statusError(&http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"1", "2"}}}))
	require.True(t, ok)
	require.Nil(t, d.RetryAfterSeconds)
	d, ok = Diagnostic(statusError(&http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)}}}))
	require.True(t, ok)
	require.NotNil(t, d.RetryAfterSeconds)
	require.InDelta(t, 3600, *d.RetryAfterSeconds, 2)
}
