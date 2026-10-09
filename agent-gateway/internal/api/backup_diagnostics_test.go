package api

import (
	"bytes"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/backup"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestBackupPartialEffectsReachDefaultOperatorSink(t *testing.T) {
	for _, mode := range []string{"published-sync", "create-audit", "delete-audit", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "gateway")
			require.NoError(t, os.Mkdir(root, 0o700))
			owner, err := gatewaypaths.Acquire(root)
			require.NoError(t, err)
			defer func() { require.NoError(t, owner.Close()) }()
			store, err := storage.Initialize(t.Context(), owner, testID)
			require.NoError(t, err)
			defer func() { require.NoError(t, store.Close()) }()
			faultCalls, cleanupCalls := 0, 0
			manager, err := backup.New(backup.Options{Ownership: owner, Store: store, Layout: owner.Layout(), Clock: custodyClock{}, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x42}, 1024)), Fault: func(point backup.FaultPoint) error {
				if mode == "cleanup" {
					if point == backup.FaultCopy {
						faultCalls++
						return errors.New("copy refused secret-idempotency-canary")
					}
					if point == backup.FaultCleanup {
						cleanupCalls++
						return errors.New("staging cleanup refused secret-idempotency-canary")
					}
				}
				if mode == "published-sync" && point == backup.FaultPublishedSync {
					faultCalls++
					return errors.New("directory durability unconfirmed secret-idempotency-canary")
				}
				return nil
			}})
			require.NoError(t, err)
			var id string
			if mode == "delete-audit" {
				item, _, err := manager.Create(t.Context(), "prepare", "prepare")
				require.NoError(t, err)
				id = item.ID
			}
			if mode == "create-audit" || mode == "delete-audit" {
				require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
					_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER refuse_backup_outcome BEFORE INSERT ON control_audit_events WHEN NEW.category='backup' AND json_extract(NEW.event,'$.phase')='outcome' BEGIN SELECT RAISE(ABORT,'outcome persistence refused'); END`)
					return err
				}))
			}
			var output bytes.Buffer
			adapter := diagnostics.New(&output, diagnostics.Warn)
			t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
			handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Backups: manager, Diagnostics: adapter})
			boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
			require.NoError(t, err)
			method, path := http.MethodPost, "/api/v2/backups"
			if mode == "delete-audit" {
				method, path = http.MethodDelete, path+"/"+id
			}
			response := perform(boundary, method, path, `{}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "Idempotency-Key": "secret-idempotency-canary"})
			require.Equal(t, 503, response.Code)
			require.NotContains(t, response.Body.String(), "persistence refused")
			require.NotContains(t, response.Body.String(), "durability unconfirmed")
			require.True(t, adapter.Finish(nil))
			require.NotContains(t, output.String(), "secret-idempotency-canary")
			require.NotContains(t, output.String(), testBearer)
			switch mode {
			case "cleanup":
				require.Equal(t, 1, faultCalls)
				require.Equal(t, 1, cleanupCalls)
				require.Contains(t, output.String(), "publication=not_started")
				require.Contains(t, output.String(), "cleanup=unconfirmed")
				require.Contains(t, output.String(), "copy refused")
				require.Contains(t, output.String(), "staging cleanup refused")
				entries, err := os.ReadDir(owner.Layout().Backups)
				require.NoError(t, err)
				require.Len(t, entries, 1)
				require.Contains(t, entries[0].Name(), ".staging")
			case "delete-audit":
				require.Contains(t, output.String(), "removal=ack directory_sync=ack outcome_audit=unconfirmed")
				_, err := os.Stat(filepath.Join(owner.Layout().Backups, id))
				require.True(t, os.IsNotExist(err))
			default:
				require.Contains(t, output.String(), "publication=renamed")
				require.Contains(t, output.String(), "cleanup=retained_published")
				items, err := manager.List(t.Context())
				require.NoError(t, err)
				require.Len(t, items, 1)
				if mode == "published-sync" {
					require.Equal(t, 1, faultCalls)
					require.Contains(t, output.String(), "directory_sync=unconfirmed")
					require.Contains(t, output.String(), "durability unconfirmed")
				} else {
					require.Contains(t, output.String(), "directory_sync=ack")
					require.Contains(t, output.String(), "outcome_audit=unconfirmed")
					require.Contains(t, output.String(), "outcome persistence refused")
				}
			}
		})
	}
}
