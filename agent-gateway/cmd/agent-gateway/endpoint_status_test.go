package main

import (
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
)

func TestControlEndpointsStatus(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		serving, started, latched, draining bool
		auth                                contract.AgentAuthMode
		traffic                             contract.TrafficStatus
		api, mcp                            contract.EndpointState
	}{
		{"starting", false, false, false, false, contract.AgentAuthPrincipalCredentials, contract.TrafficStatus{Ready: true}, contract.EndpointStarting, contract.EndpointStarting},
		{"runtime starting", true, false, false, false, contract.AgentAuthPrincipalCredentials, contract.TrafficStatus{Ready: true}, contract.EndpointReady, contract.EndpointStarting},
		{"ready", true, true, false, false, contract.AgentAuthPrincipalCredentials, contract.TrafficStatus{Ready: true}, contract.EndpointReady, contract.EndpointReady},
		{"latched", true, true, true, false, contract.AgentAuthPrincipalCredentials, contract.TrafficStatus{Ready: true}, contract.EndpointReadOnly, contract.EndpointUnavailable},
		{"draining", true, true, true, true, contract.AgentAuthPrincipalCredentials, contract.TrafficStatus{Ready: true}, contract.EndpointDraining, contract.EndpointDraining},
		{"traffic fault", true, true, false, false, contract.AgentAuthPrincipalCredentials, contract.TrafficStatus{Faulted: true}, contract.EndpointReady, contract.EndpointReady},
		{"traffic read unavailable", true, true, false, false, contract.AgentAuthPrincipalCredentials, contract.TrafficStatus{}, contract.EndpointReady, contract.EndpointReady},
		{"pressure is not outage", true, true, false, false, contract.AgentAuthPrincipalCredentials, contract.TrafficStatus{Ready: true, Pressure: true}, contract.EndpointReady, contract.EndpointReady},
		{"deny all", true, true, false, false, contract.AgentAuthDenyAll, contract.TrafficStatus{Ready: true}, contract.EndpointReady, contract.EndpointDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := controlEndpointsStatus("127.0.0.1:8210", tc.serving, tc.started, tc.latched, tc.draining, tc.auth, tc.traffic)
			assert.Equal(t, tc.api, got.API)
			assert.Equal(t, tc.mcp, got.MCP)
			assert.Equal(t, "127.0.0.1:8210", got.Authority)
		})
	}
}
