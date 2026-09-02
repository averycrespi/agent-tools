package mcpingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/discovery"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type listToolsFunc func(context.Context, *authorization.Lease, string, ToolsListEncoder) ([]byte, error)

func (function listToolsFunc) ListTools(ctx context.Context, lease *authorization.Lease, cursor string, encode ToolsListEncoder) ([]byte, error) {
	return function(ctx, lease, cursor, encode)
}

func newModernDiscoveryBoundary(t *testing.T, list ToolsListService) (*Handler, *httpboundary.Boundary, *testAuthority) {
	t.Helper()
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	handler := New(Options{Authenticator: authority, ListTools: list})
	return handler, newLegacyBoundary(t, handler), authority
}

func TestModernDiscoveryAdvertisesToolsAndWritesPagerBytes(t *testing.T) {
	t.Parallel()
	var gotCursor string
	var gotBinding authorization.CredentialBinding
	list := listToolsFunc(func(ctx context.Context, lease *authorization.Lease, cursor string, encode ToolsListEncoder) ([]byte, error) {
		gotCursor = cursor
		gotBinding = lease.Binding()
		return encode(ctx, []*discovery.Tool{{Name: "server.tool", InputSchema: json.RawMessage(`{"type":"object"}`)}}, "next-page")
	})
	_, boundary, authority := newModernDiscoveryBoundary(t, list)

	bootstrap := modernRequest(http.MethodPost, `{"jsonrpc":"2.0","id":"bootstrap","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	bootstrap.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, bootstrap)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, `{"jsonrpc":"2.0","id":"bootstrap","result":{"resultType":"complete","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"mcp-gateway","version":"s1"}},"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28","2025-11-25","2025-06-18","2025-03-26","2024-11-05"],"capabilities":{"tools":{}}}}`, strings.TrimSpace(response.Body.String()))
	assert.NotContains(t, response.Body.String(), "listChanged")

	request := modernRequest(http.MethodPost, `{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/list","params":{"cursor":"opaque-cursor","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, contract.MediaTypeJSON, response.Header().Get("Content-Type"))
	assert.Equal(t, `{"jsonrpc":"2.0","id":9007199254740993,"result":{"tools":[{"inputSchema":{"type":"object"},"name":"server.tool"}],"nextCursor":"next-page"}}`, response.Body.String())
	assert.Equal(t, "opaque-cursor", gotCursor)
	assert.Equal(t, authority.principals["valid"], gotBinding.PrincipalID)
	for _, lease := range authority.captured() {
		assert.True(t, leaseDone(lease))
	}
}

func TestModernDiscoveryMapsEveryClosedError(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "invalid params", err: ErrToolsListInvalidParams, want: `{"jsonrpc":"2.0","id":"error","error":{"code":-32602,"message":"The tools/list parameters are invalid.","data":{"code":"invalid_params"}}}`},
		{name: "stale cursor", err: ErrToolsListStaleCursor, want: `{"jsonrpc":"2.0","id":"error","error":{"code":-32001,"message":"The tools/list cursor is stale.","data":{"code":"stale_cursor"}}}`},
		{name: "authorization unavailable", err: ErrToolsListAuthorizationUnavailable, want: `{"jsonrpc":"2.0","id":"error","error":{"code":-32002,"message":"Authorization is unavailable.","data":{"code":"authorization_unavailable"}}}`},
		{name: "resource limit", err: ErrToolsListResourceLimit, want: `{"jsonrpc":"2.0","id":"error","error":{"code":-32003,"message":"The tool list exceeds a resource limit.","data":{"code":"resource_limit"}}}`},
		{name: "unknown failure closes", err: errors.New("internal detail"), want: `{"jsonrpc":"2.0","id":"error","error":{"code":-32002,"message":"Authorization is unavailable.","data":{"code":"authorization_unavailable"}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			list := listToolsFunc(func(context.Context, *authorization.Lease, string, ToolsListEncoder) ([]byte, error) {
				return nil, test.err
			})
			_, boundary, _ := newModernDiscoveryBoundary(t, list)
			request := modernRequest(http.MethodPost, `{"jsonrpc":"2.0","id":"error","method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
			request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
			response := httptest.NewRecorder()
			boundary.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Equal(t, test.want, response.Body.String())
		})
	}
}

func TestModernDiscoveryUsesRealPagerAcrossPages(t *testing.T) {
	t.Parallel()
	projected := make([]*discovery.Tool, 101)
	for index := range projected {
		projected[index] = &discovery.Tool{Name: "tool", InputSchema: json.RawMessage(`{"type":"object"}`)}
	}
	projector := fixedProjector{projection: discovery.Projection{
		Tools: projected,
		Snapshot: discovery.Snapshot{
			Generation:  catalog.CurrentGeneration{ProcessGeneration: "01ARZ3NDEKTSV4RRFFQ69G5FAY", ActiveGeneration: 1},
			PrincipalID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", PrincipalRevision: "1", Visibility: contract.VisibilityRequestable,
			CredentialID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", CredentialRevision: "1", AuthorizationRevision: "0",
			EvaluatedAt: time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC),
		},
	}}
	codec, err := discovery.NewCursorCodec(bytes.NewReader(bytes.Repeat([]byte{7}, 32)))
	require.NoError(t, err)
	pager, err := discovery.NewPager(projector, codec)
	require.NoError(t, err)
	_, boundary, _ := newModernDiscoveryBoundary(t, pagerAdapter{pager: pager})

	first := modernListRequest(1, "")
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, first)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var firstPage struct {
		Result struct {
			Tools      []discovery.Tool `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &firstPage))
	assert.Len(t, firstPage.Result.Tools, 100)
	require.NotEmpty(t, firstPage.Result.NextCursor)

	second := modernListRequest(2, firstPage.Result.NextCursor)
	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, second)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var secondPage struct {
		Result struct {
			Tools      []discovery.Tool `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &secondPage))
	assert.Len(t, secondPage.Result.Tools, 1)
	assert.Empty(t, secondPage.Result.NextCursor)
}

func TestModernDiscoveryRejectsIncompleteModernMetadataBeforeListing(t *testing.T) {
	t.Parallel()
	called := false
	list := listToolsFunc(func(context.Context, *authorization.Lease, string, ToolsListEncoder) ([]byte, error) {
		called = true
		return nil, nil
	})
	_, boundary, _ := newModernDiscoveryBoundary(t, list)
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":null}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":null,"io.modelcontextprotocol/clientCapabilities":{}}}}`,
	} {
		request := modernRequest(http.MethodPost, body)
		request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
		response := httptest.NewRecorder()
		boundary.ServeHTTP(response, request)
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}
	assert.False(t, called)
}

func TestModernDiscoveryLeaseInvalidationCancelsPager(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	list := listToolsFunc(func(ctx context.Context, _ *authorization.Lease, _ string, _ ToolsListEncoder) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	_, boundary, authority := newModernDiscoveryBoundary(t, list)
	request := modernListRequest(1, "")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		boundary.ServeHTTP(response, request)
		close(done)
	}()
	<-started
	leases := authority.captured()
	require.Len(t, leases, 1)
	leases[0].Release()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("lease invalidation did not cancel discovery")
	}
	<-done
	assert.Empty(t, response.Body.String())
	assert.True(t, leaseDone(leases[0]))
}

func TestModernDiscoveryDoesNotPublishAfterLeaseInvalidation(t *testing.T) {
	list := listToolsFunc(func(ctx context.Context, lease *authorization.Lease, _ string, encode ToolsListEncoder) ([]byte, error) {
		encoded, err := encode(ctx, make([]*discovery.Tool, 0), "")
		lease.Release()
		return encoded, err
	})
	_, boundary, authority := newModernDiscoveryBoundary(t, list)
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, modernListRequest(1, ""))
	assert.Empty(t, response.Body.String())
	leases := authority.captured()
	require.Len(t, leases, 1)
	assert.True(t, leaseDone(leases[0]))
}

func TestModernDiscoveryCredentialReplacementCancelsPager(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	list := listToolsFunc(func(ctx context.Context, _ *authorization.Lease, _ string, _ ToolsListEncoder) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	_, boundary, authority := newModernDiscoveryBoundary(t, list)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		boundary.ServeHTTP(response, modernListRequest(1, ""))
		close(done)
	}()
	<-started
	principal, err := authority.repository.GetPrincipal(t.Context(), authority.principals["valid"])
	require.NoError(t, err)
	_, err = authority.repository.IssueCredential(t.Context(), principal.ID, principal.Revision)
	require.NoError(t, err)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("credential replacement did not cancel discovery")
	}
	<-done
	assert.Empty(t, response.Body.String())
}

type fixedProjector struct {
	projection discovery.Projection
}

func (projector fixedProjector) Project(context.Context, discovery.Request) (discovery.Projection, error) {
	return projector.projection, nil
}

type pagerAdapter struct {
	pager *discovery.Pager
}

func (adapter pagerAdapter) ListTools(ctx context.Context, lease *authorization.Lease, cursor string, encode ToolsListEncoder) ([]byte, error) {
	page, err := adapter.pager.List(ctx, discovery.PageRequest{
		Lease: lease, Cursor: cursor,
		Encode: func(ctx context.Context, result discovery.ListResult) ([]byte, error) {
			return encode(ctx, result.Tools, result.NextCursor)
		},
	})
	if err != nil {
		switch {
		case errors.Is(err, discovery.ErrMalformedCursor):
			return nil, ErrToolsListInvalidParams
		case errors.Is(err, discovery.ErrStaleCursor):
			return nil, ErrToolsListStaleCursor
		case errors.Is(err, discovery.ErrResourceLimit):
			return nil, ErrToolsListResourceLimit
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, err
		default:
			return nil, ErrToolsListAuthorizationUnavailable
		}
	}
	return page.Encoded, nil
}

func modernListRequest(id int, cursor string) *http.Request {
	params := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`
	if cursor != "" {
		encoded, _ := json.Marshal(cursor)
		params = `"cursor":` + string(encoded) + `,` + params
	}
	body := `{"jsonrpc":"2.0","id":` + jsonNumber(id) + `,"method":"tools/list","params":{` + params + `}}`
	request := modernRequest(http.MethodPost, body)
	request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	return request
}

func jsonNumber(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
