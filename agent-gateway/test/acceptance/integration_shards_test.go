package acceptance

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationShardsPartitionExecutableOwnership(t *testing.T) {
	root := purposeTargetModuleRoot(t)
	for _, platform := range []struct{ os, arch string }{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}} {
		t.Run(platform.os+"/"+platform.arch, func(t *testing.T) {
			inventory, err := DiscoverSuiteInventory(root, platform.os, platform.arch)
			require.NoError(t, err)
			wanted := map[string]bool{}
			for _, test := range inventory.Tests {
				if test.Selected && test.Owner == "test-integration" {
					wanted[test.Package+"/"+test.Name] = true
				}
			}
			seen := map[string]bool{}
			packageOwners := map[string]string{}
			for _, shard := range []string{"test-integration-1", "test-integration-2"} {
				commands, err := PlanSuite(root, shard, inventory, 1)
				require.NoError(t, err)
				require.NotEmpty(t, commands)
				for _, command := range commands {
					assert.Contains(t, command.Argv, "-race")
					assert.Contains(t, command.Argv, "-count=1")
					assert.Contains(t, command.Argv, "-timeout=5m0s")
					require.NoError(t, validateSuiteCommand(root, inventory, command))
					for _, test := range command.Tests {
						key := test.Package + "/" + test.Name
						assert.False(t, seen[key], "overlap: %s", key)
						seen[key] = true
						if owner, ok := packageOwners[test.Package]; ok {
							assert.Equal(t, owner, shard, "split fixture owner %s", test.Package)
						}
						packageOwners[test.Package] = shard
					}
				}
			}
			assert.Equal(t, wanted, seen, "exact independently discovered union")
		})
	}
}

func TestIntegrationInvocationPartitionsStaySequentialAndComplete(t *testing.T) {
	root := suiteFixture(t, map[string]string{
		"agent-gateway/internal/invocation/first_test.go": "package invocation\nimport \"testing\"\nfunc TestZulu(t *testing.T) {}\nfunc TestAlpha(t *testing.T) {}\nfunc TestMiddle(t *testing.T) {}\n",
		"agent-gateway/internal/invocation/new_test.go":   "package invocation\nimport \"testing\"\nfunc TestNew(t *testing.T) {}\nfunc TestBeta(t *testing.T) {}\n",
		"agent-gateway/internal/other/other_test.go":      "package other\nimport \"testing\"\nfunc TestOther(t *testing.T) {}\n",
	})
	module := filepath.Join(root, "agent-gateway")
	inventory, err := DiscoverSuiteInventory(module, "linux", "amd64")
	require.NoError(t, err)
	commands, err := PlanSuite(module, "test-integration", inventory, 1)
	require.NoError(t, err)
	require.Len(t, commands, 2)
	seen := map[string]bool{}
	for _, command := range commands {
		require.NoError(t, validateSuiteCommand(module, inventory, command))
		assert.Contains(t, command.Argv, "-timeout=5m0s")
		assert.Contains(t, command.Argv, "-race")
		assert.Contains(t, command.Argv, "-count=1")
		for _, test := range command.Tests {
			key := test.Package + "/" + test.Name
			require.False(t, seen[key], "duplicate execution: %s", key)
			seen[key] = true
		}
	}
	require.Len(t, commands[0].Tests, 4)
	require.Len(t, commands[1].Tests, 2)
	for _, test := range inventory.Tests {
		require.True(t, seen[test.Package+"/"+test.Name], "source identity omitted")
	}
	for _, failAt := range []int{0, 1} {
		calls := 0
		var sharedDeadline time.Time
		failure := errors.New("fixture command failure")
		executor := suiteExecutorFunc(func(ctx context.Context, _ string, command Command) ([]byte, error) {
			calls++
			deadline, bounded := ctx.Deadline()
			require.True(t, bounded)
			if calls == 1 {
				sharedDeadline = deadline
			}
			assert.Equal(t, sharedDeadline, deadline, "later batches cannot renew the owner budget")
			assert.Equal(t, 12*time.Minute, command.Timeout)
			if calls == failAt {
				return nil, failure
			}
			return nil, nil
		})
		err = RunSuite(t.Context(), root, "test-integration", 1, executor)
		if failAt == 0 {
			require.NoError(t, err)
			assert.Equal(t, 2, calls)
		} else {
			require.ErrorIs(t, err, failure)
			assert.Equal(t, failAt, calls, "failure must stop later batches")
		}
	}
}

func TestIntegrationShardsIncludeNewPackagesAndRejectInvalidRequests(t *testing.T) {
	root := suiteFixture(t, map[string]string{
		"internal/invocation/first_test.go": "package invocation\nimport \"testing\"\nfunc TestFirst(t *testing.T) {}\n",
		"internal/newowner/new_test.go":     "package newowner\nimport \"testing\"\nfunc TestNew(t *testing.T) {}\n",
	})
	inventory, err := DiscoverSuiteInventory(root, "linux", "amd64")
	require.NoError(t, err)
	commands, err := PlanSuite(root, "test-integration-2", inventory, 1)
	require.NoError(t, err)
	require.Len(t, commands, 1)
	assert.Contains(t, commands[0].Argv, "./internal/newowner")
	_, err = PlanSuite(root, "test-integration-3", inventory, 1)
	require.Error(t, err)
	_, err = PlanSuite(root, "test-integration-1", inventory, 2)
	require.ErrorContains(t, err, "only stress")
}
