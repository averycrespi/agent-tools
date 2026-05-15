package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDashboardURLUsesLocalhostForDefaultLoopback(t *testing.T) {
	require.Equal(t, "http://localhost:8300/dashboard/?token=secret", dashboardURL("127.0.0.1", 8300, "secret"))
}

func TestDashboardURLUsesConfiguredLoopbackHost(t *testing.T) {
	require.Equal(t, "http://[::1]:8300/dashboard/?token=secret", dashboardURL("::1", 8300, "secret"))
}

func TestValidateDashboardHostAllowsLoopbackOnly(t *testing.T) {
	require.NoError(t, validateDashboardHost("127.0.0.1"))
	require.NoError(t, validateDashboardHost("localhost"))
	require.NoError(t, validateDashboardHost("::1"))
	require.Error(t, validateDashboardHost("0.0.0.0"))
	require.Error(t, validateDashboardHost("192.168.1.20"))
}

func TestDashboardCommandDefaults(t *testing.T) {
	require.Equal(t, "dashboard", dashboardCmd.Use)
	require.Equal(t, "Open Dispatch Board", dashboardCmd.Short)

	host, err := dashboardCmd.Flags().GetString("host")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", host)

	port, err := dashboardCmd.Flags().GetInt("port")
	require.NoError(t, err)
	require.Equal(t, 8300, port)

	noOpen, err := dashboardCmd.Flags().GetBool("no-open")
	require.NoError(t, err)
	require.False(t, noOpen)
}

func TestDashboardRequestLoggerReportsStatusWithoutQuery(t *testing.T) {
	var out bytes.Buffer
	handler := dashboardRequestLogger(&out, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/dashboard/?token=secret", nil))

	log := out.String()
	require.Contains(t, log, "request method=GET path=/dashboard/ status=202")
	require.NotContains(t, log, "secret")
	require.NotContains(t, log, "token=")
}

func TestDashboardRequestLoggerPreservesFlusher(t *testing.T) {
	var out bytes.Buffer
	handler := dashboardRequestLogger(&out, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, ok := w.(http.Flusher)
		require.True(t, ok)
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/dashboard/events", nil))

	require.Contains(t, out.String(), "path=/dashboard/events status=200")
}
