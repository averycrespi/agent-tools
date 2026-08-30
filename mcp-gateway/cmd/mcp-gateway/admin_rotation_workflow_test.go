package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rotationOldID     = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	rotationNewID     = "01BX5ZZKBKACTAV9WEVGEMMVRZ"
	rotationOldBearer = "mgw_admin_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	rotationNewBearer = "mgw_admin_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

type rotationWorkflowScenario struct {
	createMalformed     bool
	fingerprintMismatch bool
	replacement401      bool
	completionConflict  bool
	completionMalformed bool
	recoveryRevoked     bool
	finalMismatch       bool
}

type rotationWorkflowRequest struct {
	method string
	path   string
	auth   string
	etag   string
	body   string
}

func TestCLIAdminCredentialRotationWorkflow(t *testing.T) {
	root := newRootCmd()
	rotate, _, err := root.Find([]string{"admin", "credential", "rotate"})
	require.NoError(t, err)
	assert.NotNil(t, rotate.Flags().Lookup("secret-output"))
	assert.NotNil(t, rotate.Flags().Lookup("yes"))

	t.Run("happy path is ordered exact and bearer free", func(t *testing.T) {
		server, requests := newRotationWorkflowServer(t, rotationWorkflowScenario{})
		secretPath := shortRotationPath(t)
		stdout, stderr, err := executeAdminRotation(t, server.URL, secretPath, false)
		require.NoError(t, err, "%s", stderr)
		expected := `{"result":"rotated","old_credential":` + rotationCredentialJSON(rotationOldID, "3", contract.CredentialRevoked, true, rotationFingerprint(rotationOldBearer)) + `,"new_credential":` + rotationCredentialJSON(rotationNewID, "2", contract.CredentialActive, true, rotationFingerprint(rotationNewBearer)) + `}`
		assert.Equal(t, expected+"\n", string(stdout))
		assert.Empty(t, stderr)
		assert.NotContains(t, string(stdout), rotationOldBearer)
		assert.NotContains(t, string(stdout), rotationNewBearer)
		contents, readErr := os.ReadFile(secretPath)
		require.NoError(t, readErr)
		assert.Equal(t, rotationNewBearer+"\n", string(contents))
		assertRotationRequestOrder(t, *requests, []rotationWorkflowRequest{
			{method: "GET", path: "/api/v1/admin-credentials/" + rotationOldID, auth: "old"},
			{method: "GET", path: "/api/v1/admin-authority", auth: "old"},
			{method: "POST", path: "/api/v1/admin-credentials", auth: "old", etag: contract.AdminAuthorityETag("1"), body: `{"expires_at":null}`},
			{method: "GET", path: "/api/v1/admin-credentials/" + rotationNewID, auth: "new"},
			{method: "POST", path: "/api/v1/admin-credentials/" + rotationOldID + "/rotation-completion", auth: "new", etag: contract.AdminAuthorityETag("2"), body: `{"replacement_id":"` + rotationNewID + `"}`},
			{method: "GET", path: "/api/v1/admin-credentials/" + rotationOldID, auth: "new"},
			{method: "GET", path: "/api/v1/admin-credentials/" + rotationNewID, auth: "new"},
			{method: "GET", path: "/api/v1/admin-authority", auth: "new"},
		})
	})

	for _, test := range []struct {
		name          string
		scenario      rotationWorkflowScenario
		exit          int
		code          string
		requestCount  int
		fileExists    bool
		uncertain     bool
		defaultBearer bool
	}{
		{name: "create uncertainty", scenario: rotationWorkflowScenario{createMalformed: true}, exit: 8, code: "admin_rotation_create_uncertain", requestCount: 3, uncertain: true},
		{name: "publication failure", scenario: rotationWorkflowScenario{fingerprintMismatch: true}, exit: 2, code: "admin_rotation_publish_failed", requestCount: 3},
		{name: "replacement authentication failure", scenario: rotationWorkflowScenario{replacement401: true}, exit: 3, code: "admin_rotation_replacement_verification_failed", requestCount: 4, fileExists: true},
		{name: "authority conflict preserves overlap", scenario: rotationWorkflowScenario{completionConflict: true}, exit: 5, code: "stale_admin_authority", requestCount: 5, fileExists: true, defaultBearer: true},
		{name: "revoke uncertainty reads old once", scenario: rotationWorkflowScenario{completionMalformed: true}, exit: 8, code: "admin_rotation_revoke_uncertain", requestCount: 6, fileExists: true, uncertain: true},
		{name: "final verification mismatch", scenario: rotationWorkflowScenario{finalMismatch: true}, exit: 10, code: "admin_rotation_final_verification_failed", requestCount: 6, fileExists: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, requests := newRotationWorkflowServer(t, test.scenario)
			secretPath := shortRotationPath(t)
			stdout, stderr, err := executeAdminRotation(t, server.URL, secretPath, test.defaultBearer)
			require.Error(t, err)
			assert.Equal(t, test.exit, commandExitCode(err), "%s", stderr)
			assert.Empty(t, stdout)
			var problem struct {
				Code      string `json:"code"`
				Title     string `json:"title"`
				Exit      int    `json:"exit_code"`
				Uncertain bool   `json:"uncertain"`
			}
			require.NoError(t, json.Unmarshal(stderr, &problem))
			assert.Equal(t, test.code, problem.Code)
			assert.Equal(t, test.exit, problem.Exit)
			assert.Equal(t, test.uncertain, problem.Uncertain)
			assert.NotContains(t, string(stderr), rotationOldBearer)
			assert.NotContains(t, string(stderr), rotationNewBearer)
			assert.Len(t, *requests, test.requestCount)
			if test.fileExists {
				assert.FileExists(t, secretPath)
				assert.Contains(t, problem.Title, "--admin-bearer-file")
			} else {
				assert.NoFileExists(t, secretPath)
			}
			if test.defaultBearer {
				assert.Contains(t, problem.Title, "default bearer file still names the old credential")
			}
		})
	}

	t.Run("human success names safe state command and stale default", func(t *testing.T) {
		server, _ := newRotationWorkflowServer(t, rotationWorkflowScenario{})
		secretPath := shortRotationPath(t)
		stdout, stderr, err := executeAdminRotationMode(t, server.URL, secretPath, true, "human")
		require.NoError(t, err, "%s", stderr)
		assert.Contains(t, string(stdout), rotationOldID)
		assert.Contains(t, string(stdout), rotationNewID)
		assert.Contains(t, string(stdout), "--admin-bearer-file")
		assert.Contains(t, string(stdout), "default bearer file still names the old credential")
		assert.NotContains(t, string(stdout), rotationOldBearer)
		assert.NotContains(t, string(stdout), rotationNewBearer)
		assert.Empty(t, stderr)
	})

	t.Run("uncertain completion recognizes one observed revoke", func(t *testing.T) {
		server, requests := newRotationWorkflowServer(t, rotationWorkflowScenario{completionMalformed: true, recoveryRevoked: true})
		secretPath := shortRotationPath(t)
		stdout, stderr, err := executeAdminRotation(t, server.URL, secretPath, false)
		require.NoError(t, err, "%s", stderr)
		assert.Contains(t, string(stdout), `"result":"rotated"`)
		assert.Len(t, *requests, 6)
		assert.Equal(t, "/api/v1/admin-credentials/"+rotationOldID, (*requests)[5].path)
	})
}

func newRotationWorkflowServer(t *testing.T, scenario rotationWorkflowScenario) (*httptest.Server, *[]rotationWorkflowRequest) {
	t.Helper()
	requests := make([]rotationWorkflowRequest, 0, 8)
	var mutex sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(request.Body)
		auth := "unknown"
		switch request.Header.Get("Authorization") {
		case "Bearer " + rotationOldBearer:
			auth = "old"
		case "Bearer " + rotationNewBearer:
			auth = "new"
		}
		mutex.Lock()
		step := len(requests)
		requests = append(requests, rotationWorkflowRequest{method: request.Method, path: request.URL.Path, auth: auth, etag: request.Header.Get("If-Match"), body: body.String()})
		mutex.Unlock()
		writer.Header().Set("Content-Type", contract.MediaTypeJSON)
		switch step {
		case 0:
			_, _ = writer.Write([]byte(rotationCredentialJSON(rotationOldID, "1", contract.CredentialActive, true, rotationFingerprint(rotationOldBearer))))
		case 1:
			writer.Header().Set("ETag", contract.AdminAuthorityETag("1"))
			_, _ = writer.Write([]byte(`{"revision":"1"}`))
		case 2:
			if scenario.createMalformed {
				writer.WriteHeader(http.StatusCreated)
				_, _ = writer.Write([]byte(`{"invalid":true}`))
				return
			}
			fingerprint := rotationFingerprint(rotationNewBearer)
			if scenario.fingerprintMismatch {
				fingerprint = strings.Repeat("0", 16)
			}
			writer.Header().Set("ETag", contract.AdminAuthorityETag("2"))
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(strings.TrimSuffix(rotationCredentialJSON(rotationNewID, "2", contract.CredentialActive, true, fingerprint), "}") + `,"bearer":"` + rotationNewBearer + `"}`))
		case 3:
			if scenario.replacement401 {
				writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(`{"status":401,"code":"authentication_required","title":"Authentication is required."}`))
				return
			}
			_, _ = writer.Write([]byte(rotationCredentialJSON(rotationNewID, "2", contract.CredentialActive, true, rotationFingerprint(rotationNewBearer))))
		case 4:
			if scenario.completionConflict {
				writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
				writer.WriteHeader(http.StatusPreconditionFailed)
				_, _ = writer.Write([]byte(`{"status":412,"code":"stale_admin_authority","title":"The administrator authority revision is stale."}`))
				return
			}
			if scenario.completionMalformed {
				_, _ = writer.Write([]byte(`{"invalid":true}`))
				return
			}
			writer.Header().Set("ETag", contract.AdminAuthorityETag("3"))
			_, _ = writer.Write([]byte(`{"old_credential":` + rotationCredentialJSON(rotationOldID, "3", contract.CredentialRevoked, true, rotationFingerprint(rotationOldBearer)) + `,"new_credential":` + rotationCredentialJSON(rotationNewID, "2", contract.CredentialActive, true, rotationFingerprint(rotationNewBearer)) + `}`))
		case 5:
			status := contract.CredentialRevoked
			revision := "3"
			if scenario.finalMismatch || scenario.completionMalformed && !scenario.recoveryRevoked {
				status, revision = contract.CredentialActive, "1"
			}
			_, _ = writer.Write([]byte(rotationCredentialJSON(rotationOldID, revision, status, true, rotationFingerprint(rotationOldBearer))))
		case 6:
			_, _ = writer.Write([]byte(rotationCredentialJSON(rotationNewID, "2", contract.CredentialActive, true, rotationFingerprint(rotationNewBearer))))
		case 7:
			writer.Header().Set("ETag", contract.AdminAuthorityETag("3"))
			_, _ = writer.Write([]byte(`{"revision":"3"}`))
		default:
			writer.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func shortRotationPath(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "mgw-rotate-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	return filepath.Join(directory, "replacement")
}

func executeAdminRotation(t *testing.T, address, secretPath string, defaultBearer bool) ([]byte, []byte, error) {
	t.Helper()
	return executeAdminRotationMode(t, address, secretPath, defaultBearer, "json")
}

func executeAdminRotationMode(t *testing.T, address, secretPath string, defaultBearer bool, output string) ([]byte, []byte, error) {
	t.Helper()
	command := newRootCmd()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetIn(bytes.NewReader(nil))
	args := []string{"admin", "credential", "rotate", rotationOldID, "--secret-output", secretPath, "--yes", "--address", address, "--output", output}
	if defaultBearer {
		dataDir := t.TempDir()
		layout, err := gatewaypaths.Resolve(dataDir)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(layout.AdminBearer), 0o700))
		require.NoError(t, os.WriteFile(layout.AdminBearer, []byte(rotationOldBearer+"\n"), 0o600))
		args = append(args, "--data-dir", dataDir)
	} else {
		oldPath := filepath.Join(t.TempDir(), "old")
		require.NoError(t, os.WriteFile(oldPath, []byte(rotationOldBearer+"\n"), 0o600))
		args = append(args, "--admin-bearer-file", oldPath)
	}
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	return stdout.Bytes(), stderr.Bytes(), err
}

func assertRotationRequestOrder(t *testing.T, actual, expected []rotationWorkflowRequest) {
	t.Helper()
	require.Equal(t, expected, actual)
}

func rotationCredentialJSON(id, revision string, status contract.CredentialStatus, nonExpiring bool, fingerprint string) string {
	return `{"id":"` + id + `","fingerprint":"` + fingerprint + `","created_at":"2026-08-30T00:00:00Z","expires_at":null,"non_expiring":` + strconv.FormatBool(nonExpiring) + `,"status":"` + string(status) + `","revision":"` + revision + `"}`
}

func rotationFingerprint(bearer string) string {
	verifier := sha256.Sum256(append([]byte("mcp-gateway/admin-verifier/v1\x00"), bearer...))
	digest := sha256.Sum256(append([]byte("mcp-gateway/admin-fingerprint/v1\x00"), verifier[:]...))
	return hex.EncodeToString(digest[:8])
}
