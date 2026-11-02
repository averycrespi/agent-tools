package composition

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func initializationFixture(t *testing.T) (*gatewaypaths.Ownership, gatewaypaths.InitializationPlan) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := gatewaypaths.AcquireInitialization(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	plan := gatewaypaths.InitializationPlan{Version: 1, Root: root, Bearer: owner.Layout().AdminBearer, Installation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Traffic: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
	require.NoError(t, gatewaypaths.BeginInitialization(owner, plan))
	return owner, plan
}

func TestInitializationReconcilesCompletedEffectsWithoutDuplication(t *testing.T) {
	for _, boundary := range []string{"control", "custody", "traffic", "administrator", "ca", "certificate", "complete"} {
		for _, suffix := range []string{"-effect", "-sealed"} {
			t.Run(boundary+suffix, func(t *testing.T) {
				owner, plan := initializationFixture(t)
				clock := testutil.NewFakeClock(compositionTime)
				injected := errors.New("injected interruption")
				_, err := initializeOwned(t.Context(), owner, plan, clock, rand.Reader, func(point string) error {
					if point == boundary+suffix {
						return injected
					}
					return nil
				})
				require.ErrorIs(t, err, injected)
				retained := map[string][]byte{}
				for _, path := range []string{gatewaypaths.MasterKeyName, gatewaypaths.AdminBearerName, gatewaypaths.PublicCertificateName} {
					data, err := os.ReadFile(filepath.Join(plan.Root, path))
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
					require.NoError(t, err)
					retained[path] = data
				}
				_, complete, err := gatewaypaths.ReadInitialization(owner.Layout())
				require.NoError(t, err)
				if !complete {
					_, err = InitializeOwned(t.Context(), owner, plan, clock, rand.Reader)
					require.NoError(t, err)
				}
				for path, data := range retained {
					current, err := os.ReadFile(filepath.Join(plan.Root, path))
					require.NoError(t, err)
					require.Equal(t, data, current, "preserve "+path)
				}
				_, complete, err = gatewaypaths.ReadInitialization(owner.Layout())
				require.NoError(t, err)
				require.True(t, complete)
				// Repeated resume cannot create another administrator, custody or CA.
				before, err := os.ReadFile(owner.Layout().Database)
				require.NoError(t, err)
				_, err = InitializeOwned(t.Context(), owner, plan, clock, rand.Reader)
				require.Error(t, err)
				after, err := os.ReadFile(owner.Layout().Database)
				require.NoError(t, err)
				require.Equal(t, before, after)
				_, err = storage.InspectMaintenance(t.Context(), owner, func(tx *sql.Tx) error {
					for _, table := range []string{"admin_credentials", "secret_custody", "keyring_authorities"} {
						var count int
						require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count))
						require.Equal(t, 1, count, table)
					}
					return nil
				})
				require.NoError(t, err)
			})
		}
	}
}

func TestInitializationPreservesChangedAndMissingEstablishedFiles(t *testing.T) {
	for _, scenario := range []string{"missing-key", "wrong-key", "wrong-bearer", "wrong-certificate", "unknown-file", "missing-control", "missing-traffic"} {
		t.Run(scenario, func(t *testing.T) {
			owner, plan := initializationFixture(t)
			clock := testutil.NewFakeClock(compositionTime)
			_, err := initializeOwned(t.Context(), owner, plan, clock, rand.Reader, func(point string) error {
				if point == "certificate-sealed" {
					return errors.New("stop")
				}
				return nil
			})
			require.Error(t, err)
			path := ""
			switch scenario {
			case "missing-key":
				path = filepath.Join(plan.Root, gatewaypaths.MasterKeyName)
				require.NoError(t, os.Remove(path))
			case "wrong-key":
				path = filepath.Join(plan.Root, gatewaypaths.MasterKeyName)
				require.NoError(t, os.WriteFile(path, make([]byte, 32), 0600))
			case "wrong-bearer":
				path = plan.Bearer
				require.NoError(t, os.WriteFile(path, []byte("unrelated"), 0600))
			case "wrong-certificate":
				path = filepath.Join(plan.Root, gatewaypaths.PublicCertificateName)
				require.NoError(t, os.WriteFile(path, []byte("unrelated"), 0600))
			case "unknown-file":
				path = filepath.Join(plan.Root, "operator-notes")
				require.NoError(t, os.WriteFile(path, []byte("retain"), 0600))
			case "missing-control":
				path = owner.Layout().Database
				require.NoError(t, os.Remove(path))
			case "missing-traffic":
				path = filepath.Join(plan.Root, "traffic-"+plan.Traffic+".db")
				require.NoError(t, os.Remove(path))
			}
			before := initializationTree(t, plan.Root)
			_, err = InitializeOwned(t.Context(), owner, plan, clock, rand.Reader)
			require.Error(t, err)
			require.Equal(t, before, initializationTree(t, plan.Root))
		})
	}
}

func TestInitializationRefusesLostUnboundKeyAfterAttempt(t *testing.T) {
	owner, plan := initializationFixture(t)
	clock := testutil.NewFakeClock(compositionTime)
	_, err := initializeOwned(t.Context(), owner, plan, clock, rand.Reader, func(point string) error {
		if point == "custody-attempt" {
			return errors.New("interrupted publication")
		}
		return nil
	})
	require.Error(t, err)
	before := initializationTree(t, plan.Root)
	_, err = InitializeOwned(t.Context(), owner, plan, clock, rand.Reader)
	require.Error(t, err)
	require.Equal(t, before, initializationTree(t, plan.Root), "missing uncertain key must not authorize a new key")
}

func TestInitializationRefusesUncertainBearerPublication(t *testing.T) {
	owner, plan := initializationFixture(t)
	clock := testutil.NewFakeClock(compositionTime)
	_, err := initializeOwned(t.Context(), owner, plan, clock, rand.Reader, func(point string) error {
		if point == "traffic-sealed" {
			return errors.New("stop")
		}
		return nil
	})
	require.Error(t, err)
	store, err := storage.Open(t.Context(), owner)
	require.NoError(t, err)
	// The sink retains a published bearer but fails before activation. Its durable
	// audit attempt must block creation even if the file is later missing.
	_, err = admin.NewService(store, clock, rand.Reader).Initialize(t.Context(), failedInitializationSink{plan.Bearer})
	require.Error(t, err)
	require.NoError(t, store.Close())
	before := initializationTree(t, plan.Root)
	_, err = InitializeOwned(t.Context(), owner, plan, clock, rand.Reader)
	require.Error(t, err)
	require.Equal(t, before, initializationTree(t, plan.Root))
	require.NoError(t, os.Remove(plan.Bearer))
	before = initializationTree(t, plan.Root)
	_, err = InitializeOwned(t.Context(), owner, plan, clock, rand.Reader)
	require.Error(t, err)
	require.Equal(t, before, initializationTree(t, plan.Root))
}

type failedInitializationSink struct{ path string }

func (s failedInitializationSink) Publish(value string) error {
	if err := initializationSink(s).Publish(value); err != nil {
		return err
	}
	return errors.New("lost publication acknowledgement")
}

func initializationTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[path] = initializationDigest(data)
		return nil
	}))
	return result
}
