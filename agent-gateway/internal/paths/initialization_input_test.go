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

func TestInitializationInputAndImportReceiptOperationErrors(t *testing.T) {
	for _, target := range []string{"input", "import"} {
		for _, mode := range []string{"publish", "reseal"} {
			for _, operation := range []string{"file-sync", "file-close", "directory-sync", "root-sync"} {
				t.Run(target+"/"+mode+"/"+operation, func(t *testing.T) {
					owner, plan := initializationPublicationFixture(t)
					input := []byte(`{"configuration":"validated private input"}`)
					plan.ImportDigest = fmt.Sprintf("%x", sha256.Sum256(input))
					require.NoError(t, BeginInitialization(owner, plan))
					if target == "import" || mode == "reseal" {
						require.NoError(t, RetainInitializationInput(owner, plan, input))
					}
					if target == "import" && mode == "reseal" {
						require.NoError(t, SealInitialization(owner, "import", plan.ImportDigest))
					}
					injected := errors.New("filesystem operation failed")
					calls := 0
					publication := initializationPublication{}
					switch operation {
					case "file-sync":
						publication.syncFile = func(*os.File) error { calls++; return injected }
					case "file-close":
						publication.closeFile = func(*os.File) error { calls++; return injected }
					default:
						publication.syncDir = func(path string) error {
							selected := filepath.Join(plan.Root, InitializationName)
							if operation == "root-sync" {
								selected = plan.Root
							}
							if path == selected {
								calls++
								return injected
							}
							return syncDirectory(path)
						}
					}
					var err error
					switch {
					case mode == "reseal":
						err = syncInitializationFile(owner.Layout(), target, publication)
					case target == "input":
						err = retainInitializationInput(owner, plan, input, publication)
					default:
						err = sealInitialization(owner, "import", plan.ImportDigest, publication)
					}
					require.ErrorIs(t, err, injected)
					require.Equal(t, 1, calls, "failure must be returned by the operation, not a post-success acknowledgement hook")
					before := initializationEvidenceBytes(t, plan.Root)
					require.NoError(t, owner.Close())
					reopened, err := AcquireStoppedExisting(plan.Root)
					require.NoError(t, err)
					defer func() { require.NoError(t, reopened.Close()) }()
					require.NoError(t, syncInitializationFile(reopened.Layout(), target, initializationPublication{}))
					if target == "input" {
						actual, err := ReadInitializationInput(reopened.Layout(), plan)
						require.NoError(t, err)
						require.Equal(t, input, actual)
						require.Error(t, RetainInitializationInput(reopened, plan, input))
					} else {
						require.NoError(t, SealInitialization(reopened, "import", plan.ImportDigest))
					}
					require.Equal(t, before, initializationEvidenceBytes(t, plan.Root), "reseal never replaces evidence")
				})
			}
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

func TestInitializationInputRemovalDirectoryOperationError(t *testing.T) {
	owner, plan := completedImportPublicationFixture(t)
	injected := errors.New("directory fsync failed")
	calls := 0
	require.ErrorIs(t, removeInitializationInput(owner, initializationPublication{syncDir: func(path string) error {
		if path == filepath.Join(plan.Root, InitializationName) {
			calls++
			if calls == 2 {
				return injected
			}
		}
		return syncDirectory(path)
	}}), injected)
	require.Equal(t, 2, calls, "completion reseal must precede cleanup directory sync failure")
	_, err := os.Lstat(filepath.Join(plan.Root, InitializationName, "input"))
	require.ErrorIs(t, err, os.ErrNotExist)
	proof, err := InitializationReceipt(owner.Layout(), "input-removed")
	require.NoError(t, err)
	require.Empty(t, proof)
	require.NoError(t, RemoveInitializationInput(owner))
	proof, err = InitializationReceipt(owner.Layout(), "input-removed")
	require.NoError(t, err)
	require.Equal(t, plan.ImportDigest, proof)
}

func TestInitializationInputCleanupReceiptPublicationFailures(t *testing.T) {
	for _, point := range []string{"create", "short-write", "write-error", "write", "file-sync", "close", "directory-sync", "root-sync"} {
		t.Run(point, func(t *testing.T) {
			owner, plan := completedImportPublicationFixture(t)
			injected := errors.New("cleanup proof publication failed")
			publication := initializationPublication{fault: func(actual string) error {
				if actual == "input-removed:"+point {
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
			err := removeInitializationInput(owner, publication)
			if point == "short-write" {
				require.ErrorIs(t, err, io.ErrShortWrite)
			} else {
				require.ErrorIs(t, err, injected)
			}
			_, err = os.Lstat(filepath.Join(plan.Root, InitializationName, "input"))
			require.ErrorIs(t, err, os.ErrNotExist)
			before := initializationEvidenceBytes(t, plan.Root)
			err = RemoveInitializationInput(owner)
			if point == "create" || point == "short-write" || point == "write-error" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, before, initializationEvidenceBytes(t, plan.Root), "never overwrite a cleanup publication attempt")
		})
	}
}

func TestInitializationInputCleanupResealsReadableCompletionBeforeUnlink(t *testing.T) {
	for _, operation := range []string{"file-sync", "file-close", "directory-sync", "root-sync"} {
		t.Run(operation, func(t *testing.T) {
			owner, plan := preparedImportPublicationFixture(t)
			injected := errors.New("completion file sync failed")
			require.ErrorIs(t, sealInitialization(owner, "complete", "complete", initializationPublication{syncFile: func(*os.File) error { return injected }}), injected)
			_, complete, err := ReadInitialization(owner.Layout())
			require.NoError(t, err)
			require.True(t, complete, "readable bytes do not prove acknowledged durability")
			inputPath := filepath.Join(plan.Root, InitializationName, "input")
			input, err := os.ReadFile(inputPath)
			require.NoError(t, err)
			failed := errors.New("completion reseal operation failed")
			publication := initializationPublication{}
			switch operation {
			case "file-sync":
				publication.syncFile = func(*os.File) error { return failed }
			case "file-close":
				publication.closeFile = func(*os.File) error { return failed }
			default:
				publication.syncDir = func(path string) error {
					target := filepath.Join(plan.Root, InitializationName)
					if operation == "root-sync" {
						target = plan.Root
					}
					if path == target {
						return failed
					}
					return syncDirectory(path)
				}
			}
			require.ErrorIs(t, removeInitializationInput(owner, publication), failed)
			actual, err := os.ReadFile(inputPath)
			require.NoError(t, err)
			require.Equal(t, input, actual, "failed completion durability confirmation must retain input")
			proof, err := InitializationReceipt(owner.Layout(), "input-removed")
			require.NoError(t, err)
			require.Empty(t, proof)
			require.NoError(t, owner.Close())
			reopened, err := AcquireStoppedExisting(plan.Root)
			require.NoError(t, err)
			defer func() { require.NoError(t, reopened.Close()) }()
			require.NoError(t, RemoveInitializationInput(reopened))
			_, err = os.Lstat(inputPath)
			require.ErrorIs(t, err, os.ErrNotExist)
			proof, err = InitializationReceipt(reopened.Layout(), "input-removed")
			require.NoError(t, err)
			require.Equal(t, plan.ImportDigest, proof)
		})
	}
}

func completedImportPublicationFixture(t *testing.T) (*Ownership, InitializationPlan) {
	t.Helper()
	owner, plan := preparedImportPublicationFixture(t)
	require.NoError(t, SealInitialization(owner, "complete", "complete"))
	return owner, plan
}

func preparedImportPublicationFixture(t *testing.T) (*Ownership, InitializationPlan) {
	t.Helper()
	owner, plan := initializationPublicationFixture(t)
	input := []byte(`{"configuration":"retained"}`)
	plan.ImportDigest = fmt.Sprintf("%x", sha256.Sum256(input))
	require.NoError(t, BeginInitialization(owner, plan))
	require.NoError(t, RetainInitializationInput(owner, plan, input))
	for _, step := range []struct{ name, evidence string }{{"control", plan.Installation}, {"custody-attempt", "attempt"}, {"custody", strings.Repeat("1", 64)}, {"traffic", plan.Traffic}, {"administrator", strings.Repeat("2", 64)}, {"ca", strings.Repeat("3", 64)}, {"certificate", strings.Repeat("3", 64)}, {"import", plan.ImportDigest}} {
		require.NoError(t, SealInitialization(owner, step.name, step.evidence))
	}
	return owner, plan
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
