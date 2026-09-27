//go:build integration

package httpproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPCompletionTimestampPrecision(t *testing.T) {
	for _, tc := range []struct {
		name  string
		nanos time.Duration
	}{
		{"seconds", 0},
		{"microseconds", 123456000},
		{"trailing_zero_nanoseconds", 123456780},
		{"nanoseconds", 123456789},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Keep completion after admission while exercising clock precision,
			// independently of the host OS clock's actual granularity.
			completed := time.Now().UTC().Add(time.Minute).Truncate(time.Second).Add(tc.nanos)
			f := fixtureWithCompletionClock(t, func() time.Time { return completed })
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "completed")
			}))
			defer upstream.Close()
			f.allow(t, upstream.URL, "allow_requests", "", "")
			response := f.request(t, f.client(t), http.MethodGet, upstream.URL, nil)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, "completed", string(body))
			require.Eventually(t, func() bool {
				history, err := f.traffic.HTTPHistory(t.Context(), 0, 10)
				return err == nil && len(history.Records) == 1 && history.Records[0].Completion != nil
			}, 3*time.Second, 10*time.Millisecond)
			history, err := f.traffic.HTTPHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			completion := history.Records[0].Completion
			require.Equal(t, "succeeded", completion.Outcome)
			require.Equal(t, "upstream", completion.ResponseSource)
			require.Equal(t, http.StatusOK, completion.Status)
			require.Zero(t, completion.GatewayStatus)
			require.Equal(t, completed.Format("2006-01-02T15:04:05.000000000Z07:00"), completion.CompletedAt)
		})
	}
}
