//go:build darwin || linux

package paths

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitializationEvidenceIsBoundedAndFencesStartup(t *testing.T) {
	for _, scenario := range []string{"incomplete", "partial-plan", "partial-receipt", "out-of-order", "unknown-entry", "symlink", "hardlink", "unsafe-mode", "wrong-root", "too-large"} {
		t.Run(scenario, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "gateway")
			owner, err := AcquireInitialization(root)
			require.NoError(t, err)
			defer func() { require.NoError(t, owner.Close()) }()
			plan := InitializationPlan{Version: 1, Root: root, Bearer: owner.Layout().AdminBearer, Installation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Traffic: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
			require.NoError(t, BeginInitialization(owner, plan))
			directory := filepath.Join(root, InitializationName)
			path := filepath.Join(directory, "plan")
			switch scenario {
			case "partial-plan":
				require.NoError(t, os.WriteFile(path, []byte("{"), 0600))
			case "partial-receipt":
				require.NoError(t, os.WriteFile(filepath.Join(directory, "control"), []byte("partial"), 0600))
			case "out-of-order":
				require.NoError(t, os.WriteFile(filepath.Join(directory, "complete"), []byte("complete"), 0600))
			case "unknown-entry":
				require.NoError(t, os.WriteFile(filepath.Join(directory, "unknown"), []byte("retain"), 0600))
			case "symlink":
				require.NoError(t, os.Rename(path, path+"-retained"))
				require.NoError(t, os.Symlink(path+"-retained", path))
			case "hardlink":
				require.NoError(t, os.Link(path, filepath.Join(t.TempDir(), "link")))
			case "unsafe-mode":
				require.NoError(t, os.Chmod(path, 0644))
			case "wrong-root":
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, append(data, []byte("{}")...), 0600))
			case "too-large":
				require.NoError(t, os.WriteFile(path, make([]byte, 16385), 0600))
			}
			_, complete, err := ReadInitialization(owner.Layout())
			require.False(t, complete)
			if scenario == "incomplete" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.ErrorIs(t, RequireInitializationComplete(owner.Layout()), ErrInitializationIncomplete)
			require.NoError(t, owner.Close())
			_, err = AcquireExisting(root)
			require.ErrorIs(t, err, ErrInitializationIncomplete)
			_, err = os.Stat(owner.Layout().RunMarker)
			require.True(t, os.IsNotExist(err), "refuse before run-marker publication")
		})
	}
}

func TestInitializationRejectsReplacedDirectoryIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := AcquireInitialization(root)
	require.NoError(t, err)
	plan := InitializationPlan{Version: 1, Root: root, Bearer: owner.Layout().AdminBearer, Installation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Traffic: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
	require.NoError(t, BeginInitialization(owner, plan))
	require.NoError(t, owner.Close())
	data, err := os.ReadFile(filepath.Join(root, InitializationName, "plan"))
	require.NoError(t, err)
	require.NoError(t, os.Rename(root, root+"-retained"))
	require.NoError(t, os.Mkdir(root, 0700))
	require.NoError(t, os.Mkdir(filepath.Join(root, InitializationName), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, InitializationName, "plan"), data, 0600))
	layout, err := Resolve(root)
	require.NoError(t, err)
	_, _, err = ReadInitialization(layout)
	require.ErrorIs(t, err, ErrUnsafePath)
}

func TestInitializationPlanPublicationFaultBoundaries(t *testing.T) {
	for _, point := range []string{"directory-create", "create", "short-write", "write-error", "write", "file-sync", "close", "directory-sync", "root-sync"} {
		t.Run(point, func(t *testing.T) {
			owner, plan := initializationPublicationFixture(t)
			injected := errors.New("publication acknowledgement lost")
			publication := initializationPublication{fault: func(actual string) error {
				if actual == "plan:"+point {
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
			err := beginInitialization(owner, plan, publication)
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
			observed, complete, readErr := ReadInitialization(reopened.Layout())
			require.False(t, complete)
			switch point {
			case "directory-create", "create", "short-write", "write-error":
				require.Error(t, readErr)
				require.Error(t, ConfirmInitialization(reopened, plan))
			default:
				require.NoError(t, readErr)
				require.Equal(t, plan, observed)
				require.NoError(t, ConfirmInitialization(reopened, plan), "explicit continuation reseals retained consent before effects")
			}
			require.Error(t, BeginInitialization(reopened, plan), "never replace an existing plan or directory")
			require.Equal(t, before, initializationEvidenceBytes(t, plan.Root))
			require.ErrorIs(t, RequireInitializationComplete(reopened.Layout()), ErrInitializationIncomplete)
		})
	}
}

func TestInitializationReceiptPublicationFaultBoundaries(t *testing.T) {
	steps := []struct{ name, evidence string }{{"control", "01ARZ3NDEKTSV4RRFFQ69G5FAV"}, {"custody-attempt", "attempt"}, {"custody", strings.Repeat("1", 64)}, {"traffic", "01ARZ3NDEKTSV4RRFFQ69G5FAW"}, {"administrator", strings.Repeat("2", 64)}, {"ca", strings.Repeat("3", 64)}, {"certificate", strings.Repeat("3", 64)}, {"complete", "complete"}}
	for index, target := range steps {
		for _, point := range []string{"create", "short-write", "write-error", "write", "file-sync", "close", "directory-sync", "root-sync"} {
			t.Run(target.name+"/"+point, func(t *testing.T) {
				owner, plan := initializationPublicationFixture(t)
				require.NoError(t, BeginInitialization(owner, plan))
				for _, previous := range steps[:index] {
					require.NoError(t, SealInitialization(owner, previous.name, previous.evidence))
				}
				injected := errors.New("receipt acknowledgement lost")
				publication := initializationPublication{fault: func(actual string) error {
					if actual == target.name+":"+point {
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
				err := sealInitialization(owner, target.name, target.evidence, publication)
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
				err = SealInitialization(reopened, target.name, target.evidence)
				if point == "create" || point == "short-write" || point == "write-error" {
					require.Error(t, err, "partial receipt must remain refused")
				} else {
					require.NoError(t, err, "same observed proof is resealed, not replaced")
				}
				require.Equal(t, before, initializationEvidenceBytes(t, plan.Root))
				require.Error(t, SealInitialization(reopened, target.name, "unrelated"), "never replace an observed receipt")
				require.Equal(t, before, initializationEvidenceBytes(t, plan.Root))
			})
		}
	}
}

func initializationPublicationFixture(t *testing.T) (*Ownership, InitializationPlan) {
	t.Helper()
	// Exercise macOS-style parent aliases on every host so fault selectors cannot
	// accidentally compare an input spelling with the canonical owned layout.
	parent := t.TempDir()
	alias := filepath.Join(t.TempDir(), "parent-alias")
	require.NoError(t, os.Symlink(parent, alias))
	require.NoError(t, os.Mkdir(filepath.Join(parent, "selected-parent"), 0700))
	root := filepath.Join(alias, "selected-parent", "gateway")
	owner, err := AcquireInitialization(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	return owner, InitializationPlan{Version: 1, Root: owner.Layout().Root, Bearer: owner.Layout().AdminBearer, Installation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Traffic: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
}

func initializationEvidenceBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			result[path] = "directory"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[path] = string(data)
		return nil
	}))
	return result
}

func TestInitializationExcludesConcurrentOwnersAndChangedDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := AcquireInitialization(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	_, err = AcquireInitialization(root)
	require.ErrorIs(t, err, ErrInUse)
	plan := InitializationPlan{Version: 1, Root: root, Bearer: owner.Layout().AdminBearer, Installation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Traffic: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
	require.NoError(t, os.WriteFile(filepath.Join(root, "unrelated"), []byte("retain"), 0600))
	require.ErrorIs(t, BeginInitialization(owner, plan), ErrUnsafePath)
	data, err := os.ReadFile(filepath.Join(root, "unrelated"))
	require.NoError(t, err)
	require.Equal(t, "retain", string(data))
	_, err = os.Stat(filepath.Join(root, InitializationName))
	require.True(t, os.IsNotExist(err))
}
