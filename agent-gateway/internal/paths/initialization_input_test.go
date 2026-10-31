package paths

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitializationInputAndImportReceiptPublicationFaults(t *testing.T) {
	for _, target := range []string{"input", "import"} {
		for _, point := range []string{"create", "short-write", "write-error", "write", "file-sync", "close", "directory-sync", "root-sync"} {
			t.Run(target+"/"+point, func(t *testing.T) {
				owner, plan := initializationPublicationFixture(t)
				input := []byte(`{"ordinary":"configuration is not promised secret-free"}`)
				plan.ImportDigest = fmt.Sprintf("%x", sha256.Sum256(input))
				require.NoError(t, BeginInitialization(owner, plan))
				if target == "import" {
					require.NoError(t, RetainInitializationInput(owner, plan, input))
				}
				injected := errors.New("publication acknowledgement lost")
				publication := initializationPublication{fault: func(actual string) error {
					if actual == target+":"+point {
						return injected
					}
					return nil
				}}
				if point == "short-write" {
					publication.write = func(file *os.File, data []byte) (int, error) { return file.Write(data[:len(data)/2]) }
				}
				if point == "write-error" {
					publication.write = func(file *os.File, data []byte) (int, error) {
						n, err := file.Write(data[:len(data)/2])
						return n, errors.Join(err, injected)
					}
				}
				var err error
				if target == "input" {
					err = retainInitializationInput(owner, plan, input, publication)
				} else {
					err = sealInitialization(owner, target, plan.ImportDigest, publication)
				}
				if point == "short-write" {
					require.ErrorIs(t, err, io.ErrShortWrite)
				} else {
					require.ErrorIs(t, err, injected)
				}
				before := initializationEvidenceBytes(t, plan.Root)
				require.NoError(t, owner.Close())
				reopened, err := AcquireStoppedExisting(plan.Root)
				require.NoError(t, err)
				defer func() { require.NoError(t, reopened.Close()) }()
				partial := point == "create" || point == "short-write" || point == "write-error"
				if target == "input" {
					_, _, err := ReadInitialization(reopened.Layout())
					if partial {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
						require.NoError(t, ConfirmInitialization(reopened, plan))
					}
					require.Error(t, RetainInitializationInput(reopened, plan, input), "never republish or overwrite retained input")
				} else {
					err = SealInitialization(reopened, target, plan.ImportDigest)
					if partial {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
				}
				require.Equal(t, before, initializationEvidenceBytes(t, plan.Root))
			})
		}
	}
}

func TestInitializationInputRemovalPublicationFaultsPreserveCompletion(t *testing.T) {
	for _, point := range []string{"input:before-remove", "input:remove", "input:remove-directory-sync"} {
		t.Run(point, func(t *testing.T) {
			owner, plan := initializationPublicationFixture(t)
			input := []byte(`{"configuration":"retained"}`)
			plan.ImportDigest = fmt.Sprintf("%x", sha256.Sum256(input))
			require.NoError(t, BeginInitialization(owner, plan))
			require.NoError(t, RetainInitializationInput(owner, plan, input))
			for _, step := range []struct{ name, evidence string }{{"control", plan.Installation}, {"custody-attempt", "attempt"}, {"custody", strings.Repeat("1", 64)}, {"traffic", plan.Traffic}, {"administrator", strings.Repeat("2", 64)}, {"ca", strings.Repeat("3", 64)}, {"certificate", strings.Repeat("3", 64)}, {"import", plan.ImportDigest}, {"complete", "complete"}} {
				require.NoError(t, SealInitialization(owner, step.name, step.evidence))
			}
			injected := errors.New("removal acknowledgement lost")
			require.ErrorIs(t, removeInitializationInput(owner, initializationPublication{fault: func(actual string) error {
				if actual == point {
					return injected
				}
				return nil
			}}), injected)
			_, complete, err := ReadInitialization(owner.Layout())
			require.NoError(t, err)
			require.True(t, complete)
			require.NoError(t, RemoveInitializationInput(owner))
			require.NoError(t, RemoveInitializationInput(owner))
			_, err = os.Stat(filepath.Join(plan.Root, InitializationName, "input"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestInitializationInputCleanupRequiresConfirmedCompletion(t *testing.T) {
	owner, plan := initializationPublicationFixture(t)
	input := []byte(`{"complete":"validated input"}`)
	plan.ImportDigest = fmt.Sprintf("%x", sha256.Sum256(input))
	require.NoError(t, BeginInitialization(owner, plan))
	require.NoError(t, RetainInitializationInput(owner, plan, input))
	require.ErrorIs(t, RemoveInitializationInput(owner), ErrInitializationIncomplete)
	for _, step := range []struct{ name, evidence string }{{"control", plan.Installation}, {"custody-attempt", "attempt"}, {"custody", strings.Repeat("1", 64)}, {"traffic", plan.Traffic}, {"administrator", strings.Repeat("2", 64)}, {"ca", strings.Repeat("3", 64)}, {"certificate", strings.Repeat("3", 64)}, {"import", plan.ImportDigest}, {"complete", "complete"}} {
		require.NoError(t, SealInitialization(owner, step.name, step.evidence))
	}
	require.NoError(t, RemoveInitializationInput(owner))
	require.NoError(t, RemoveInitializationInput(owner))
	_, err := os.Stat(filepath.Join(plan.Root, InitializationName, "input"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, complete, err := ReadInitialization(owner.Layout())
	require.NoError(t, err)
	require.True(t, complete)
}
