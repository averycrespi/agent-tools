package api

import (
	"crypto/rand"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/backup"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

type custodyClock struct{}

func (custodyClock) Now() time.Time { return time.Now() }

func TestEncryptedBackupRefusalIsExplicitConflict(t *testing.T) {
	owner, err := gatewaypaths.AcquireForMaintenance(filepath.Join(t.TempDir(), "gateway"))
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	store, err := storage.Initialize(t.Context(), owner, testID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	require.NoError(t, keyring.SetupCustody(t.Context(), owner, store, custodyClock{}))
	manager, err := backup.New(backup.Options{Store: store, Layout: owner.Layout(), Clock: custodyClock{}, Entropy: rand.Reader})
	require.NoError(t, err)
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Backups: manager, Invalidate: func(contract.Invalidation) { t.Fatal("refusal published success invalidation") }})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	response := perform(boundary, http.MethodPost, "/api/v2/backups", `{}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "Idempotency-Key": "encrypted-refusal"})
	require.Equal(t, http.StatusConflict, response.Code)
	require.Contains(t, response.Body.String(), `"code":"encrypted_backup_unsupported"`)
	require.Contains(t, response.Body.String(), "Preserve existing artifacts")
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
}
