package main

import (
	"bytes"
	"context"
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
	require.Equal(t, "Open Pi Dispatch Dashboard", dashboardCmd.Short)

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

func TestDashboardRequestLoggerSkipsSuccessfulRequestsByDefault(t *testing.T) {
	var out bytes.Buffer
	handler := dashboardRequestLogger(&out, false, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/dashboard/?token=secret", nil))

	require.Empty(t, out.String())
}

func TestDashboardRequestLoggerReportsFailedRequestsByDefault(t *testing.T) {
	var out bytes.Buffer
	handler := dashboardRequestLogger(&out, false, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/dashboard/?token=secret", nil))

	log := out.String()
	require.Contains(t, log, "request method=GET path=/dashboard/ status=500")
	require.NotContains(t, log, "pd dashboard:")
	require.NotContains(t, log, "secret")
	require.NotContains(t, log, "token=")
}

func TestDashboardLogfOmitsCommandPrefix(t *testing.T) {
	var out bytes.Buffer

	dashboardLogf(&out, "listening addr=%s", "127.0.0.1:8300")

	require.Equal(t, "listening addr=127.0.0.1:8300\n", out.String())
}

func TestDashboardRequestLoggerReportsSuccessfulRequestsWhenVerbose(t *testing.T) {
	var out bytes.Buffer
	handler := dashboardRequestLogger(&out, true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/dashboard/?token=secret", nil))

	require.Contains(t, out.String(), "request method=GET path=/dashboard/ status=202")
}

func TestDashboardRequestLoggerPreservesFlusher(t *testing.T) {
	var out bytes.Buffer
	handler := dashboardRequestLogger(&out, false, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, ok := w.(http.Flusher)
		require.True(t, ok)
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/dashboard/events", nil))

	require.Empty(t, out.String())
}

func TestDashboardShutdownFirstSignalGracefulSecondSignalForced(t *testing.T) {
	signals := make(chan struct{}, 2)
	shutdownStarted := make(chan struct{})
	allowShutdownReturn := make(chan struct{})
	forced := make(chan struct{})
	var logged []string

	done := make(chan error, 1)
	go func() {
		done <- waitForDashboardShutdown(
			signals,
			make(chan error),
			func(_ context.Context) error {
				close(shutdownStarted)
				<-allowShutdownReturn
				return context.Canceled
			},
			func(format string, args ...any) { logged = append(logged, format) },
			func() { close(forced) },
		)
	}()

	signals <- struct{}{}
	<-shutdownStarted
	signals <- struct{}{}
	<-forced
	close(allowShutdownReturn)

	require.ErrorIs(t, <-done, context.Canceled)
	require.Contains(t, logged, "shutting down, send Ctrl-C again to force exit")
	require.Contains(t, logged, "forced shutdown")
}
