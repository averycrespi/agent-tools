//go:build e2e

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestE2E_StrictRoleBoundaryAndPublicEndpoints(t *testing.T) {
	s := newTestStack(t, stackOpts{Tools: defaultTools})

	tests := []struct {
		name       string
		method     string
		path       string
		token      string
		body       string
		wantStatus int
	}{
		{name: "agent cannot read dashboard", method: http.MethodGet, path: "/dashboard/api/tools", token: s.AgentToken, wantStatus: http.StatusFound},
		{name: "admin reads dashboard API", method: http.MethodGet, path: "/dashboard/api/tools", token: s.AdminToken, wantStatus: http.StatusOK},
		{name: "agent cannot read dashboard asset", method: http.MethodGet, path: "/dashboard/favicon.svg", token: s.AgentToken, wantStatus: http.StatusFound},
		{name: "admin reads dashboard asset", method: http.MethodGet, path: "/dashboard/favicon.svg", token: s.AdminToken, wantStatus: http.StatusOK},
		{name: "agent cannot decide", method: http.MethodPost, path: "/dashboard/api/decide", token: s.AgentToken, body: `{}`, wantStatus: http.StatusFound},
		{name: "admin reaches decision handler", method: http.MethodPost, path: "/dashboard/api/decide", token: s.AdminToken, body: `{}`, wantStatus: http.StatusNotFound},
		{name: "agent cannot use root catch all", method: http.MethodGet, path: "/", token: s.AgentToken, wantStatus: http.StatusUnauthorized},
		{name: "admin uses root redirect", method: http.MethodGet, path: "/", token: s.AdminToken, wantStatus: http.StatusFound},
		{name: "agent cannot use unknown catch all", method: http.MethodGet, path: "/unknown", token: s.AgentToken, wantStatus: http.StatusUnauthorized},
		{name: "admin uses unknown catch all", method: http.MethodGet, path: "/unknown", token: s.AdminToken, wantStatus: http.StatusFound},
		{name: "admin cannot call MCP", method: http.MethodPost, path: "/mcp", token: s.AdminToken, body: `{}`, wantStatus: http.StatusUnauthorized},
		{name: "health is public", method: http.MethodGet, path: "/healthz", wantStatus: http.StatusOK},
		{name: "unauthorized page is public", method: http.MethodGet, path: "/dashboard/unauthorized", wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantStatus, requestStatus(t, s.BrokerURL+tt.path, tt.method, tt.token, tt.body, nil))
		})
	}
}

func TestE2E_DashboardBootstrapCookieContainsOnlyAdminToken(t *testing.T) {
	s := newTestStack(t, stackOpts{Tools: defaultTools})
	client := noRedirectClient()
	resp, err := client.Get(s.BrokerURL + "/dashboard/?token=" + s.AdminToken + "&view=audit")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "/dashboard/?view=audit", resp.Header.Get("Location"))
	require.Len(t, resp.Cookies(), 1)
	cookie := resp.Cookies()[0]
	if cookie.Value != s.AdminToken {
		t.Fatal("dashboard cookie did not contain the admin credential")
	}
	if cookie.Value == s.AgentToken {
		t.Fatal("dashboard cookie contained the agent credential")
	}

	require.Equal(t, http.StatusOK, requestStatus(t, s.BrokerURL+"/dashboard/api/tools", http.MethodGet, "", "", cookie))
	require.Equal(t, http.StatusFound, requestStatus(t, s.BrokerURL+"/dashboard/api/tools", http.MethodGet, "", "", &http.Cookie{Name: cookie.Name, Value: s.AgentToken}))

	agentBootstrap, err := client.Get(s.BrokerURL + "/dashboard/?token=" + s.AgentToken)
	require.NoError(t, err)
	defer agentBootstrap.Body.Close()
	require.Equal(t, http.StatusFound, agentBootstrap.StatusCode)
	require.Equal(t, "/dashboard/unauthorized", agentBootstrap.Header.Get("Location"))
	require.Empty(t, agentBootstrap.Cookies())
}

func TestE2E_LegacyMigrationPreservesAgentAndNonTerminalOutputConfinesTokens(t *testing.T) {
	legacy := strings.Repeat("a", 64)
	s := newTestStack(t, stackOpts{Tools: defaultTools, LegacyToken: "\n" + legacy + "\t"})
	if s.AgentToken != legacy {
		t.Fatal("legacy credential was not preserved as the canonical agent credential")
	}
	if s.AdminToken == legacy {
		t.Fatal("migration reused the legacy credential as the admin credential")
	}
	require.NoFileExists(t, filepath.Join(s.ConfigDir, "auth-token"))

	output, err := os.ReadFile(s.OutputPath)
	require.NoError(t, err)
	if bytes.Contains(output, []byte(s.AgentToken)) {
		t.Fatal("non-terminal broker output contained the agent credential")
	}
	if bytes.Contains(output, []byte(s.AdminToken)) {
		t.Fatal("non-terminal broker output contained the admin credential")
	}
}

func TestE2E_AdminRotationLeavesAuthenticatedSSEOpenButRejectsNewRequests(t *testing.T) {
	s := newTestStack(t, stackOpts{
		Tools: defaultTools,
		Rules: []testRuleConfig{{Tool: "*", Verdict: "allow"}},
	})
	request, err := http.NewRequest(http.MethodGet, s.BrokerURL+"/dashboard/events", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+s.AdminToken)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	reader := bufio.NewReader(response.Body)
	_, err = reader.ReadString('\n')
	require.NoError(t, err)
	_, err = reader.ReadString('\n')
	require.NoError(t, err)

	newAdmin := strings.Repeat("d", 64)
	require.NoError(t, os.WriteFile(filepath.Join(s.ConfigDir, "admin-token"), []byte(newAdmin), 0o600))
	require.NoError(t, s.brokerCmd.Process.Signal(syscall.SIGHUP))
	waitForStatus(t, s.BrokerURL+"/dashboard/api/tools", http.MethodGet, newAdmin, http.StatusOK)
	require.Equal(t, http.StatusFound, requestStatus(t, s.BrokerURL+"/dashboard/api/tools", http.MethodGet, s.AdminToken, "", nil))

	_, err = s.callTool("echo.say_hello", nil)
	require.NoError(t, err)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "data: "))
}

func TestE2E_IndependentRoleReloadAndFailureRetention(t *testing.T) {
	s := newTestStack(t, stackOpts{Tools: defaultTools})
	oldAgent := s.AgentToken
	oldAdmin := s.AdminToken
	newAgent := strings.Repeat("c", 64)
	newAdmin := strings.Repeat("d", 64)
	finalAdmin := strings.Repeat("e", 64)

	require.NoError(t, os.WriteFile(filepath.Join(s.ConfigDir, "agent-token"), []byte(newAgent), 0o600))
	require.NoError(t, os.WriteFile(s.RulesPath, []byte(`{"rules":[`), 0o600))
	require.NoError(t, s.brokerCmd.Process.Signal(syscall.SIGHUP))
	waitForStatus(t, s.BrokerURL+"/mcp", http.MethodGet, oldAgent, http.StatusUnauthorized)

	newClient := newMCPClient(t, s.BrokerURL, newAgent)
	require.Equal(t, http.StatusOK, requestStatus(t, s.BrokerURL+"/dashboard/api/tools", http.MethodGet, oldAdmin, "", nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := s.Client.ListTools(ctx, gomcp.ListToolsRequest{})
	cancel()
	require.Error(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(s.ConfigDir, "admin-token"), []byte(newAdmin), 0o600))
	require.NoError(t, s.brokerCmd.Process.Signal(syscall.SIGHUP))
	waitForStatus(t, s.BrokerURL+"/dashboard/api/tools", http.MethodGet, newAdmin, http.StatusOK)
	require.Equal(t, http.StatusFound, requestStatus(t, s.BrokerURL+"/dashboard/api/tools", http.MethodGet, oldAdmin, "", nil))
	listTools(t, newClient)

	require.NoError(t, os.WriteFile(filepath.Join(s.ConfigDir, "agent-token"), []byte("malformed"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(s.ConfigDir, "admin-token"), []byte(finalAdmin), 0o600))
	require.NoError(t, s.brokerCmd.Process.Signal(syscall.SIGHUP))
	waitForStatus(t, s.BrokerURL+"/dashboard/api/tools", http.MethodGet, finalAdmin, http.StatusOK)
	listTools(t, newClient)
	require.Equal(t, http.StatusFound, requestStatus(t, s.BrokerURL+"/dashboard/api/tools", http.MethodGet, newAdmin, "", nil))
}

func newMCPClient(t *testing.T, brokerURL, token string) *client.Client {
	t.Helper()
	mcpClient, err := client.NewStreamableHttpClient(brokerURL+"/mcp", transport.WithHTTPHeaders(map[string]string{
		"Authorization": "Bearer " + token,
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mcpClient.Close() })
	request := gomcp.InitializeRequest{}
	request.Params.ProtocolVersion = gomcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = gomcp.Implementation{Name: "auth-e2e-test", Version: "0.0.1"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = mcpClient.Initialize(ctx, request)
	require.NoError(t, err)
	return mcpClient
}

func listTools(t *testing.T, mcpClient *client.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := mcpClient.ListTools(ctx, gomcp.ListToolsRequest{})
	require.NoError(t, err)
}

func waitForStatus(t *testing.T, target, method, token string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if requestStatus(t, target, method, token, "", nil) == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("request did not reach status %d", want)
}

func requestStatus(t *testing.T, target, method, token, body string, cookie *http.Cookie) int {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := noRedirectClient().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	return response.StatusCode
}

func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}
