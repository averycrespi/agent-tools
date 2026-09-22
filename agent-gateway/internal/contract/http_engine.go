package contract

import "time"

// HTTP engine limits are independent of MCP execution occupancy. They are
// correctness bounds, not a capacity qualification or throughput promise.
const (
	HTTPProxyConnections    = 256
	HTTPProxyWork           = 128
	HTTPProxyPrincipalWork  = 96
	HTTPProxyH2Streams      = 32
	HTTPProxyBufferBytes    = 32 * 1024
	HTTPProxyHeaderBytes    = 32 * 1024
	HTTPProxyDialTimeout    = 10 * time.Second
	HTTPProxyHeaderTimeout  = 15 * time.Second
	HTTPProxyIdleTimeout    = 60 * time.Second
	HTTPProxyDrainTimeout   = 10 * time.Second
	HTTPProxyTunnelLifetime = time.Hour
	HTTPCAEnvelopeBytes     = 4096
	HTTPCAEntries           = 256
	HTTPCALifetime          = 5 * 365 * 24 * time.Hour
	HTTPLeafLifetime        = 24 * time.Hour
)
