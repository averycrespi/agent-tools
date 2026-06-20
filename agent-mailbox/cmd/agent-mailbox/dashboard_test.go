package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateDashboardHostAcceptsLoopbackAllowlist(t *testing.T) {
	require.NoError(t, validateDashboardHost("127.0.0.1"))
	require.NoError(t, validateDashboardHost("localhost"))
	require.NoError(t, validateDashboardHost("::1"))
}

func TestValidateDashboardHostRejectsUnsafeHosts(t *testing.T) {
	for _, host := range []string{"", "0.0.0.0", "::", "example.com", "192.168.1.1"} {
		t.Run(host, func(t *testing.T) {
			require.Error(t, validateDashboardHost(host))
		})
	}
}
