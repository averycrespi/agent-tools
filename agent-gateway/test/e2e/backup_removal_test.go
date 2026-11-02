//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestRetiredBackupArtifactsDoNotGateStartupOrStatus(t *testing.T) {
	for _, inventory := range []string{"malformed-tree", "symlink", "opaque-file"} {
		t.Run(inventory, func(t *testing.T) {
			h := newGatewayHarness(t)
			retained := testutil.RetiredBackupArtifacts(t, h.root)
			if inventory != "malformed-tree" {
				// Move only this disposable fixture tree and retain its fingerprint too.
				old := retained[0]
				archived := filepath.Join(h.root, "historical-inventory")
				require.NoError(t, os.Rename(old, archived))
				retained = append(retained, archived)
				if inventory == "symlink" {
					require.NoError(t, os.Symlink(archived, old))
				} else {
					require.NoError(t, os.WriteFile(old, []byte("not a backup directory"), 0o644))
				}
			}
			before := testutil.FingerprintArtifacts(t, retained)
			for _, args := range [][]string{{"init", "--data-dir", h.root}, {"init", "--resume", "--confirm", "--data-dir", h.root}} {
				result, err := h.runner.Run(h.ctx, h.binary, args...)
				require.NoError(t, err, "%s", result.Stderr)
				assertSettledResult(t, result)
				require.Equal(t, before, testutil.FingerprintArtifacts(t, retained))
			}
			diagnostic, _ := h.runner.Run(h.ctx, h.binary, "doctor", "--verify-storage", "--json", "--data-dir", h.root)
			assertSettledResult(t, diagnostic)
			var checklist struct {
				Checks []struct {
					Name  string `json:"name"`
					State string `json:"state"`
				} `json:"checks"`
			}
			require.NoError(t, json.Unmarshal(diagnostic.Stdout, &checklist))
			inspected := false
			for _, check := range checklist.Checks {
				if check.Name == "storage inspection" {
					require.Equal(t, "ok", check.State)
					inspected = true
				}
			}
			require.True(t, inspected)
			require.Equal(t, before, testutil.FingerprintArtifacts(t, retained))
			for run := 0; run < 2; run++ {
				h.Start()
				status := h.adminSnapshot(http.MethodGet, "/api/v2/system-status", nil)
				require.Equal(t, http.StatusOK, status.StatusCode, string(status.Body))
				var value map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(status.Body, &value))
				require.NotContains(t, value, "backup")
				var process struct {
					Ready bool `json:"ready"`
				}
				require.NoError(t, json.Unmarshal(value["process"], &process))
				require.True(t, process.Ready)
				var limits map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(value["limits"], &limits))
				for _, key := range []string{"backup_work", "backup_records", "idempotency_records"} {
					require.NotContains(t, limits, key)
				}
				for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
					for _, path := range []string{"/api/v2/backups", "/api/v2/backups/01ARZ3NDEKTSV4RRFFQ69G5FAV"} {
						removed := h.adminSnapshot(method, path, nil)
						require.Equal(t, http.StatusNotFound, removed.StatusCode, "%s %s: %s", method, path, removed.Body)
					}
				}
				h.Stop(syscall.SIGTERM)
				require.Equal(t, before, testutil.FingerprintArtifacts(t, retained))
			}
		})
	}
}
