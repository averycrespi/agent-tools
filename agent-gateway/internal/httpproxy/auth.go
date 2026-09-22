package httpproxy

import (
	"encoding/base64"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// proxyBearer accepts only one bounded credential. Basic uses the fixed username
// "agent" and the existing agent bearer as its password, not another authority.
func proxyBearer(values []string) (string, bool) {
	if len(values) != 1 || len(values[0]) > 1024 {
		return "", false
	}
	scheme, value, ok := strings.Cut(values[0], " ")
	if !ok || value == "" {
		return "", false
	}
	if strings.EqualFold(scheme, "Bearer") {
		return value, true
	}
	if !strings.EqualFold(scheme, "Basic") {
		return "", false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil {
		return "", false
	}
	defer clear(decoded)
	username, password, ok := strings.Cut(string(decoded), ":")
	return password, ok && username == "agent" && password != "" && !strings.ContainsAny(password, ":\r\n\t ")
}

func (e *Engine) Status() contract.HTTPProxyStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return contract.HTTPProxyStatus{Enabled: true, CAReady: e.options.Signer.Ready(), Ready: !e.draining, Connections: contract.LimitStatus{InUse: int64(len(e.connections)), Limit: contract.HTTPProxyConnections, Saturated: len(e.connections) >= contract.HTTPProxyConnections}, Work: contract.LimitStatus{InUse: int64(e.work), Limit: contract.HTTPProxyWork, Saturated: e.work >= contract.HTTPProxyWork}, ActiveStreams: int64(e.streams), ActiveTunnels: int64(e.tunnels)}
}
