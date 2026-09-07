package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func suiteFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, contents := range files {
		path = filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	}
	return root
}

func TestSuiteInventoryClosesTaggedHolesAndPreservesBuildIdentity(t *testing.T) {
	root := suiteFixture(t, map[string]string{
		"internal/contract/pure_test.go":            "package contract\nimport \"testing\"\nfunc TestPure(t *testing.T) {}\nfunc FuzzSeed(f *testing.F) {}\nfunc ExampleValue() {\n// Output: value\n}\n",
		"internal/db/sqlite_test.go":                "package db\nimport \"testing\"\nfunc TestDatabase(t *testing.T) {}\n",
		"internal/db/sqlite_integration_test.go":    "//go:build integration\n\npackage db\nimport \"testing\"\nfunc TestMigration(t *testing.T) {}\n",
		"internal/composition/provider_e2e_test.go": "//go:build e2e\n\npackage composition\nimport \"testing\"\nfunc TestProvider(t *testing.T) {}\n",
		"internal/db/platform_darwin_test.go":       "//go:build darwin\n\npackage db\nimport \"testing\"\nfunc TestPlatform(t *testing.T) {}\n",
	})
	inventory, err := DiscoverSuiteInventory(root, "linux", "arm64")
	require.NoError(t, err)
	owners := make(map[string]string)
	for _, test := range inventory.Tests {
		if test.Selected {
			owners[test.Name] = test.Owner
		}
	}
	assert.Equal(t, map[string]string{"TestPure": "test-unit", "FuzzSeed": "test-unit", "ExampleValue": "test-unit", "TestDatabase": "test-integration", "TestMigration": "test-integration", "TestProvider": "test-e2e"}, owners)
	plan, err := PlanSuite(root, "test-integration", inventory, 1)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	assert.Contains(t, plan[0].Argv, "-race")
	assert.Contains(t, plan[0].Argv, "-count=1")
	assert.Contains(t, plan[0].Argv, "-tags=integration")
	assert.Equal(t, "^(TestDatabase|TestMigration)$", plan[0].Argv[slices.Index(plan[0].Argv, "-run")+1])
	assert.NotContains(t, plan[0].Argv, "./internal/contract")
	darwin, err := DiscoverSuiteInventory(root, "darwin", "arm64")
	require.NoError(t, err)
	assert.True(t, slices.ContainsFunc(darwin.Tests, func(test SuiteTest) bool { return test.Name == "TestPlatform" && test.Selected }))
}

func TestSuiteInventoryRejectsUnownedConflictingAndUnselectedTags(t *testing.T) {
	for _, tag := range []string{"forgotten", "integration && e2e", "!integration", "e2e && forgotten", "browser"} {
		t.Run(tag, func(t *testing.T) {
			root := suiteFixture(t, map[string]string{"internal/db/orphan_test.go": "//go:build " + tag + "\n\npackage db\nimport \"testing\"\nfunc TestOrphan(t *testing.T) {}\n"})
			_, err := DiscoverSuiteInventory(root, "linux", "arm64")
			require.Error(t, err)
		})
	}
}

func TestSuitePlansRejectEmptyRepeatedAndDuplicateSelections(t *testing.T) {
	root := suiteFixture(t, map[string]string{"internal/db/db_test.go": "package db\nimport \"testing\"\nfunc TestDatabase(t *testing.T) {}\n"})
	inventory, err := DiscoverSuiteInventory(root, "linux", "arm64")
	require.NoError(t, err)
	_, err = PlanSuite(root, "test-e2e", inventory, 1)
	require.ErrorContains(t, err, "empty")
	_, err = PlanSuite(root, "test-integration", inventory, 2)
	require.ErrorContains(t, err, "only stress")
	inventory.Tests = append(inventory.Tests, inventory.Tests[0])
	_, err = PlanSuite(root, "test-integration", inventory, 1)
	require.ErrorContains(t, err, "duplicate executable identity")
}

func TestSuitePlansDoNotReselectOrdinaryTestsInTaggedPackages(t *testing.T) {
	root := suiteFixture(t, map[string]string{
		"test/security/guard_test.go":      "//go:build security\n\npackage security\nimport \"testing\"\nfunc TestSharedName(t *testing.T) {}\n",
		"test/acceptance/ordinary_test.go": "package acceptance\nimport \"testing\"\nfunc TestSharedName(t *testing.T) {}\n",
		"test/acceptance/security_test.go": "//go:build security\n\npackage acceptance\nimport \"testing\"\nfunc TestReport(t *testing.T) {}\n",
	})
	inventory, err := DiscoverSuiteInventory(root, "linux", "arm64")
	require.NoError(t, err)
	plan, err := PlanSuite(root, "test-security", inventory, 1)
	require.NoError(t, err)
	require.Len(t, plan, 2, "a shared global regex would also run the ordinary harness test")
	for _, command := range plan {
		require.NoError(t, validateSuiteCommand(root, inventory, command))
	}
}

func TestRepositorySuitesHaveCompleteUniqueExecutableOwnership(t *testing.T) {
	root := purposeTargetModuleRoot(t)
	inventory, err := DiscoverSuiteInventory(root, runtime.GOOS, runtime.GOARCH)
	require.NoError(t, err)
	owners := []string{"test-unit", "test-integration", "test-harness", "test-material", "test-e2e", "test-security", "test-stress", "test-keyring-native", "test-browser-workflows", "test-browser-privacy", "test-browser-visual", "test-browser-accessibility", "test-browser-cross", "test-frontend-development-browser", "frontend-static-tests"}
	selected := make(map[string]string)
	var stress []string
	for _, owner := range owners {
		plan, err := PlanSuite(root, owner, inventory, 1)
		require.NoError(t, err, owner)
		for _, command := range plan {
			require.NoError(t, validateSuiteCommand(root, inventory, command))
			for _, test := range command.Tests {
				key := test.Package + "/" + test.Name
				assert.NotContains(t, selected, key, "duplicate across owners")
				selected[key] = owner
				if owner == "test-stress" {
					stress = append(stress, test.Name)
				}
			}
		}
	}
	for _, test := range inventory.Tests {
		if test.Selected {
			assert.Equal(t, test.Owner, selected[test.Package+"/"+test.Name], test.File)
		}
	}
	assert.ElementsMatch(t, purposeStressTestIDs, stress)
	assert.True(t, slices.ContainsFunc(inventory.Tests, func(test SuiteTest) bool {
		return test.Selected && test.Package == "./internal/composition" && test.Owner == "test-e2e"
	}))
	assert.Equal(t, "test-integration", selected["./cmd/mcp-gateway/TestCLIControlBoundary"])
	assert.Equal(t, "test-harness", selected["./test/e2e/TestGatewayHarnessCleansProcessesAndBoundsTimeoutOutput"])
	for _, test := range inventory.Tests {
		if test.Owner != "test-unit" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(root, test.File))
		require.NoError(t, err)
		for _, dependency := range []string{"internal/storage\"", "internal/testutil/storagefixture\"", "\"os/exec\"", "\"database/sql\""} {
			assert.NotContains(t, string(contents), dependency, "resource owner cannot enter advertised fast unit path: %s", test.File)
		}
	}
}

func TestSuiteExecutorPreservesOuterCleanupLedger(t *testing.T) {
	ledger, err := testutil.NewCleanupLedger(t.TempDir())
	require.NoError(t, err)
	t.Setenv(testutil.CleanupLedgerEnvironment, ledger.Path())
	runner, err := testutil.NewBinaryRunner(20*time.Second, 4096)
	require.NoError(t, err)
	parent, input, err := runner.StartWithInputPipe(t.Context(), "cat")
	require.NoError(t, err)
	defer func() { _ = parent.Stop() }()
	defer func() { _ = input.Close() }()
	require.Len(t, ledger.Survivors(), 1)
	_, err = (SuiteExecutor{}).Run(t.Context(), t.TempDir(), Command{Name: "go", Arguments: []string{"version"}, Timeout: 10 * time.Second})
	require.NoError(t, err)
	require.Len(t, ledger.Survivors(), 1, "a nested suite must not terminate its outer acceptance owner")
	_, err = input.WriteString("parent remains alive\n")
	require.NoError(t, err)
	require.NoError(t, input.Close())
	result, err := parent.Wait()
	require.NoError(t, err)
	assert.Equal(t, "parent remains alive\n", string(result.Stdout))
	assert.True(t, result.Cleanup.Reaped)
	assert.Empty(t, ledger.Survivors())
}

func TestSuiteOutputWriteFailureDoesNotPass(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	t.Cleanup(func() { require.NoError(t, writer.Close()) })
	_, err = runOSCommand(t.Context(), repositoryRoot(t), Command{Name: "sh", Arguments: []string{"-c", "printf evidence"}}, false, writer)
	require.ErrorIs(t, err, syscall.EPIPE)
}

type browserSuiteExecutor struct {
	calls  []Command
	failAt int
	cancel context.CancelFunc
}

func (executor *browserSuiteExecutor) Run(_ context.Context, _ string, command Command) ([]byte, error) {
	executor.calls = append(executor.calls, command)
	if executor.cancel != nil {
		executor.cancel()
	}
	if len(executor.calls) == executor.failAt {
		return nil, errors.New("browser leaf failed")
	}
	return nil, nil
}

func TestBrowserAggregateUsesDisjointLeavesAndStopsOnFailure(t *testing.T) {
	moduleRoot := filepath.Join(repositoryRoot(t), "mcp-gateway")
	inventory, err := DiscoverSuiteInventory(moduleRoot, runtime.GOOS, runtime.GOARCH)
	require.NoError(t, err)
	for _, failAt := range []int{0, 2} {
		t.Run(fmt.Sprintf("failure_at_%d", failAt), func(t *testing.T) {
			executor := &browserSuiteExecutor{failAt: failAt}
			err := RunSuite(t.Context(), repositoryRoot(t), "test-browser", 1, executor)
			want := purposeEvidenceDAG().Aggregates["test-browser"]
			if failAt == 0 {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "browser leaf failed")
				want = want[:failAt]
			}
			require.Len(t, executor.calls, len(want))
			for index, command := range executor.calls {
				assert.Equal(t, want[index], command.CheckName)
				assert.Contains(t, command.Arguments, "-race")
				assert.Contains(t, command.Arguments, "-count=1")
				plan, err := PlanSuite(moduleRoot, want[index], inventory, 1)
				require.NoError(t, err)
				require.Len(t, plan, 1)
				assert.Equal(t, plan[0].Argv, append([]string{command.Name}, command.Arguments...))
			}
		})
	}
}

func TestBrowserAggregateCancellationStopsLaterLeaves(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	executor := &browserSuiteExecutor{cancel: cancel}
	require.ErrorIs(t, RunSuite(ctx, repositoryRoot(t), "test-browser", 1, executor), context.Canceled)
	assert.Len(t, executor.calls, 1)
}

func TestSuiteRunNativeBoundaryPrecedesPlanning(t *testing.T) {
	t.Setenv("MCP_GATEWAY_KEYRING_NATIVE", "")
	executor := &browserSuiteExecutor{}
	require.ErrorContains(t, RunSuite(t.Context(), t.TempDir(), "test-keyring-native", 1, executor), "native execution requires")
	assert.Empty(t, executor.calls)
}

type suiteExecutorFunc func(context.Context, string, Command) ([]byte, error)

func (execute suiteExecutorFunc) Run(ctx context.Context, root string, command Command) ([]byte, error) {
	return execute(ctx, root, command)
}

func TestSuiteRunnerKeepsPackageAndCommandDeadlinesSeparate(t *testing.T) {
	root := suiteFixture(t, map[string]string{
		"mcp-gateway/internal/first/first_test.go":   "package first\nimport \"testing\"\nfunc TestFirst(t *testing.T) {}\n",
		"mcp-gateway/internal/second/second_test.go": "package second\nimport \"testing\"\nfunc TestSecond(t *testing.T) {}\n",
	})
	for _, budget := range []time.Duration{0, 20 * time.Minute} {
		t.Run(budget.String(), func(t *testing.T) {
			ctx := t.Context()
			if budget != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, budget)
				defer cancel()
			}
			wantDeadline, wantBound := ctx.Deadline()
			calls := 0
			executor := suiteExecutorFunc(func(commandContext context.Context, _ string, command Command) ([]byte, error) {
				calls++
				deadline, bounded := commandContext.Deadline()
				assert.Equal(t, wantBound, bounded, "the package timeout must not bound compilation and the whole package group")
				assert.Equal(t, wantDeadline, deadline)
				assert.Contains(t, command.Arguments, "-timeout=5m0s")
				assert.Contains(t, command.Arguments, "-race")
				assert.Contains(t, command.Arguments, "-count=1")
				assert.Contains(t, command.Arguments, "./internal/first")
				assert.Contains(t, command.Arguments, "./internal/second")
				return nil, nil
			})
			require.NoError(t, RunSuite(ctx, root, "test-integration", 1, executor))
			assert.Equal(t, 1, calls)
		})
	}
}

type failingSuiteExecutor struct{ calls int }

func (executor *failingSuiteExecutor) Run(_ context.Context, _ string, command Command) ([]byte, error) {
	executor.calls++
	if command.Name != "go" || !slices.Contains(command.Arguments, "-race") {
		return nil, errors.New("incorrect command")
	}
	return nil, errors.New("fixture command failed")
}

func TestSuiteRunnerPropagatesFailureWithoutExecutingLaterGroups(t *testing.T) {
	executor := &failingSuiteExecutor{}
	err := RunSuite(t.Context(), repositoryRoot(t), "test-harness", 1, executor)
	require.ErrorContains(t, err, "fixture command failed")
	assert.Equal(t, 1, executor.calls)
	_, err = DiscoverSuiteInventory(filepath.Join(t.TempDir(), "missing"), runtime.GOOS, runtime.GOARCH)
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), "fixture command failed"))
}
