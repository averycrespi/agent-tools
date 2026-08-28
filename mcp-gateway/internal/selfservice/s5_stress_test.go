//go:build stress

package selfservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/discovery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS5StressSyntheticSnapshotPagination(t *testing.T) {
	const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAA"
	descriptors := make([]contract.ToolDescriptor, 2048)
	for index := range descriptors {
		prefix := "a"
		if index >= 2002 {
			prefix = "z"
		}
		name := fmt.Sprintf("%s%04d", prefix, index)
		schema := `{"type":"object"}`
		if index >= 2002 {
			schema = `{"type":"object","description":"` + strings.Repeat("x", 90*1024) + `"}`
		}
		descriptors[index] = contract.ToolDescriptor{
			ID: fmt.Sprintf("%026d", index+100), ServerID: serverID, UpstreamName: name, ExternalName: prefix + "." + name,
			Descriptor: contract.NormalizedToolDescriptor{Name: name, InputSchema: json.RawMessage(schema), Annotations: contract.NormalizedToolAnnotations{DestructiveHint: true, OpenWorldHint: true}},
		}
	}
	generation := catalog.CurrentGeneration{ProcessGeneration: "01ARZ3NDEKTSV4RRFFQ69G5FAB", ActiveGeneration: 8}
	policy := stressPolicySource{view: authorization.DiscoveryPolicy{
		Binding: authorization.CredentialBinding{
			PrincipalID: "01ARZ3NDEKTSV4RRFFQ69G5FAC", PrincipalRevision: "3", Visibility: contract.VisibilityAll,
			CredentialID: "01ARZ3NDEKTSV4RRFFQ69G5FAD", CredentialRevision: "4", CredentialFingerprint: "0123456789abcdef",
		},
		AuthorizationRevision: "7",
	}}
	catalogs := stressCatalogSource{snapshot: catalog.CurrentSnapshot{Generation: generation, Descriptors: descriptors}}
	service, err := discovery.NewWithSyntheticCatalog(policy, catalogs, stressClock{})
	require.NoError(t, err)
	projection, err := service.Project(context.Background(), discovery.Request{})
	require.NoError(t, err)
	require.Len(t, projection.Tools, 2054)
	codec, err := discovery.NewCursorCodec(bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32)))
	require.NoError(t, err)
	_, err = codec.Encode(discovery.CursorState{Snapshot: projection.Snapshot, Method: discovery.CursorMethodToolsList, Position: 1})
	require.NoError(t, err, "snapshot=%+v", projection.Snapshot)
	pager, err := discovery.NewPager(service, codec)
	require.NoError(t, err)

	cursor := ""
	seen := make(map[string]struct{}, 2054)
	maximumPosition := uint32(0)
	for {
		var encodings []string
		page, listErr := pager.List(context.Background(), discovery.PageRequest{Cursor: cursor, Encode: func(_ context.Context, result discovery.ListResult) ([]byte, error) {
			encoded, encodeErr := json.Marshal(struct {
				JSONRPC string               `json:"jsonrpc"`
				ID      int                  `json:"id"`
				Result  discovery.ListResult `json:"result"`
			}{JSONRPC: "2.0", ID: 17, Result: result})
			encodings = append(encodings, fmt.Sprintf("tools=%d cursor=%t bytes=%d err=%v", len(result.Tools), result.NextCursor != "", len(encoded), encodeErr))
			return encoded, encodeErr
		}})
		require.NoError(t, listErr, "cursor=%q seen=%d encodings=%v", cursor, len(seen), encodings)
		for _, tool := range page.Result.Tools {
			seen[tool.Name] = struct{}{}
		}
		cursor = page.Result.NextCursor
		if cursor == "" {
			break
		}
		state, decodeErr := codec.Decode(cursor, discovery.CursorMethodToolsList)
		require.NoError(t, decodeErr)
		maximumPosition = max(maximumPosition, state.Position)
	}
	assert.Len(t, seen, 2054)
	assert.Greater(t, maximumPosition, uint32(2048))
	for _, name := range []string{"mcp_gateway.cancel_grant_request", "mcp_gateway.create_grant_request", "mcp_gateway.get_grant_request", "mcp_gateway.get_identity", "mcp_gateway.list_grant_requests", "mcp_gateway.list_grants"} {
		assert.Contains(t, seen, name)
	}
}

func TestS5StressLostLocalMutationResponseDeduplicatesExplicitRetry(t *testing.T) {
	TestS5DrainSelfServiceReportsPostCommitUncertaintyWithoutTerminalReplay(t)
}

type stressPolicySource struct {
	view authorization.DiscoveryPolicy
}

func (source stressPolicySource) LoadDiscoveryPolicy(_ context.Context, _ *authorization.Lease, evaluatedAt time.Time) (authorization.DiscoveryPolicy, error) {
	view := source.view
	view.EvaluatedAt = evaluatedAt
	return view, nil
}

type stressCatalogSource struct {
	snapshot catalog.CurrentSnapshot
}

func (source stressCatalogSource) CurrentSnapshot() catalog.CurrentSnapshot {
	return source.snapshot
}

func (source stressCatalogSource) IsCurrentGeneration(generation catalog.CurrentGeneration) bool {
	return generation == source.snapshot.Generation
}

type stressClock struct{}

func (stressClock) Now() time.Time {
	return time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
}
