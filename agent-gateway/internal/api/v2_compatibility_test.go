package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestV1AdministrationIsRejectedBeforeAuthorityOrWork(t *testing.T) {
	calls := 0
	boundary, err := httpboundary.New(httpboundary.Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, _ *http.Request, _ contract.CredentialAuthority) (context.Context, error) {
			calls++
			return ctx, nil
		},
		Next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }),
	})
	require.NoError(t, err)
	parameters := regexp.MustCompile(`\{[^}]+\}`)
	for _, route := range contract.Routes() {
		if !strings.HasPrefix(route.Pattern, "/api/v2/") {
			continue
		}
		old := strings.ReplaceAll(route.Pattern, "/api/v2/mcp/", "/api/v1/")
		old = strings.ReplaceAll(old, "/api/v2/", "/api/v1/")
		old = strings.ReplaceAll(old, "/oauth-flows", "/auth-flows")
		old = parameters.ReplaceAllString(old, testID)
		for _, method := range route.Methods {
			t.Run(method+" "+old, func(t *testing.T) {
				response := perform(boundary, method, old, "", nil)
				require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
				require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
				require.Zero(t, calls)
			})
		}
	}
}

func TestV2PreservesPreCutoverIdempotencyRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	store, err := storage.Initialize(t.Context(), owner, testID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	clock := testutil.NewFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	var entropy []byte
	for index := 0; index < 64; index++ {
		digest := sha256.Sum256([]byte(fmt.Sprintf("v2-compatibility-%d", index)))
		entropy = append(entropy, digest[:]...)
	}
	repository, err := servers.New(store, clock, testutil.NewFakeEntropy(entropy))
	require.NoError(t, err)
	// Frozen v1 wire bytes and persisted route identities, not v2 contract helpers.
	const body = `{"namespace":"legacy","display_name":"Legacy","enabled":false,"transport":{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}}`
	const operationBody = `{"kind":"retry"}`
	transport := contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}
	created, err := repository.Create(t.Context(), servers.CreateRequest{
		Definition:  servers.Definition{Namespace: "legacy", DisplayName: "Legacy", Transport: transport},
		Idempotency: &servers.IdempotencyRequest{AuthorityID: testID, Method: "POST", Route: "/api/v1/servers", Key: "legacy-create", RequestHash: sha256.Sum256([]byte(body))},
	})
	require.NoError(t, err)
	id := created.Server.ID
	etag := `"server-` + id + `-1"`
	operation, err := repository.CreateOperation(t.Context(), servers.OperationRequest{
		ServerID: id, Kind: contract.OperationRetry, ExpectedDesiredRevision: "1",
		Idempotency: &servers.IdempotencyRequest{AuthorityID: testID, Method: "POST", Route: "/api/v1/servers/" + id + "/operations", Key: "legacy-operation", RequestHash: sha256.Sum256([]byte(operationBody)), Precondition: etag},
	})
	require.NoError(t, err)
	_, err = repository.TransitionOperation(t.Context(), operation.Operation.ID, contract.OperationRunning, nil)
	require.NoError(t, err)
	reason := contract.ReasonInterrupted
	_, err = repository.TransitionOperation(t.Context(), operation.Operation.ID, contract.OperationInterrupted, &reason)
	require.NoError(t, err)
	triggered := 0
	handler := newServerTestHandlerWithTrigger(t, repository, func(string, *string, bool) { triggered++ })
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "Idempotency-Key": "legacy-create"}
	response := perform(handler, http.MethodPost, "/api/v2/mcp/servers", body, headers)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "/api/v2/mcp/servers/"+id, response.Header().Get("Location"))
	var replay struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &replay))
	require.Equal(t, id, replay.Server.ID)
	conflict := perform(handler, http.MethodPost, "/api/v2/mcp/servers", strings.Replace(body, "Legacy", "Changed", 1), headers)
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	headers["Idempotency-Key"], headers["If-Match"] = "legacy-operation", etag
	path := "/api/v2/mcp/servers/" + id + "/operations"
	response = perform(handler, http.MethodPost, path, operationBody, headers)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var retained contract.ServerOperationMutation
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &retained))
	require.Equal(t, operation.Operation.ID, retained.Operation.ID)
	require.Equal(t, contract.OperationInterrupted, retained.Operation.State)
	for _, changed := range []struct{ body, etag string }{{`{"kind":"reload"}`, etag}, {operationBody, `"server-` + id + `-2"`}} {
		headers["If-Match"] = changed.etag
		response = perform(handler, http.MethodPost, path, changed.body, headers)
		require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "idempotency_conflict")
	}
	require.Zero(t, triggered, "retained outcomes must not schedule fresh work")
	require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
		var records, operations int
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM s2_idempotency`).Scan(&records); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM server_operations`).Scan(&operations); err != nil {
			return err
		}
		require.Equal(t, 2, records)
		require.Equal(t, 1, operations)
		return nil
	}))
}
