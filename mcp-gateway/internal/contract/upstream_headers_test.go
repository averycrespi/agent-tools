package contract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUpstreamHeaderValidation(t *testing.T) {
	for _, headers := range []map[string]string{nil, {}, {"X-MCP-Toolsets": "default,actions,gists,issues,labels,pull_requests,users"}, {"X-Empty": ""}, {strings.Repeat("x", 128): strings.Repeat("v", 4096)}} {
		assert.Empty(t, ValidateUpstreamHeaders(headers))
	}
	for _, name := range []string{"", "bad name", "name:", "é", "bad\r\nname"} {
		assert.Equal(t, ServerConfigurationRuleInvalid, ValidateUpstreamHeaders(map[string]string{name: "value"}), name)
	}
	for _, value := range []string{"a\r\nb", "a\x00b", "a\x7fb", "é", "a\tb", " leading", "trailing "} {
		assert.Equal(t, ServerConfigurationRuleInvalid, ValidateUpstreamHeaders(map[string]string{"X-Test": value}))
	}
	assert.Equal(t, ServerConfigurationRuleUnique, ValidateUpstreamHeaders(map[string]string{"X-Test": "a", "x-test": "b"}))
	for _, name := range []string{
		"Authorization", "Authentication-Info", "WWW-Authenticate", "Cookie", "Cookie2", "Set-Cookie", "Set-Cookie2", "Proxy-Authorization", "Proxy-Authenticate", "Proxy-Connection", "Sec-Fetch-Site",
		"Api-Key", "X-Api-Key", "X-Auth-Token", "X-Access-Token", "X-Authorization", "Host", "Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade", "Expect",
		"Forwarded", "Via", "X-Forwarded-Host", "X-Real-IP", "X-Original-URL", "X-Rewrite-URL", "X-HTTP-Method-Override", "Origin", "Referer", "Accept", "Accept-Encoding", "User-Agent", "Range", "If-Match", "Cache-Control", "Pragma", "Max-Forwards", "Date", "Content-Type", "Content-Length",
		"MCP-Protocol-Version", "Mcp-Session-Id", "Mcp-Method", "Mcp-Name", "Mcp-Param-Region",
	} {
		assert.Equal(t, ServerConfigurationRuleDisjoint, ValidateUpstreamHeaders(map[string]string{strings.ToUpper(name): "value"}), name)
	}
}

func TestUpstreamHeaderLimitBoundaries(t *testing.T) {
	headers := map[string]string{}
	for index := range 16 {
		headers[fmt.Sprintf("X-%d", index)] = "value"
	}
	assert.Empty(t, ValidateUpstreamHeaders(headers))
	headers["X-Extra"] = "value"
	assert.Equal(t, ServerConfigurationRuleMaximum, ValidateUpstreamHeaders(headers))
	assert.Equal(t, ServerConfigurationRuleMaximum, ValidateUpstreamHeaders(map[string]string{strings.Repeat("x", 129): ""}))
	assert.Equal(t, ServerConfigurationRuleMaximum, ValidateUpstreamHeaders(map[string]string{"X": strings.Repeat("v", 4097)}))
	headers = map[string]string{"X-A": strings.Repeat("v", 4096), "X-B": strings.Repeat("v", 4090)}
	assert.Empty(t, ValidateUpstreamHeaders(headers))
	headers["X-B"] += "v"
	assert.Equal(t, ServerConfigurationRuleMaximum, ValidateUpstreamHeaders(headers))
}
