package acceptance

import (
	"context"
	"errors"
	"fmt"
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
			for _, shard := range []string{"test-integration-1", "test-integration-2", "test-integration-3"} {
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
			unsharded, err := PlanSuite(root, "test-integration", inventory, 1)
			require.NoError(t, err)
			unshardedSeen := map[string]bool{}
			for _, command := range unsharded {
				require.NoError(t, validateSuiteCommand(root, inventory, command))
				for _, test := range command.Tests {
					key := test.Package + "/" + test.Name
					require.False(t, unshardedSeen[key], "unsharded duplicate: %s", key)
					unshardedSeen[key] = true
				}
			}
			assert.Equal(t, wanted, unshardedSeen, "unsharded exact independently discovered union")
			assert.Equal(t, "test-integration-2", packageOwners["./internal/catalog"])
			assert.Equal(t, "test-integration-1", packageOwners["./internal/audit"])
			assert.NotContains(t, packageOwners, "./internal/backup")
			assert.Equal(t, "test-integration-3", packageOwners["./internal/keyring"])
			assert.Equal(t, "test-integration-3", packageOwners["./internal/authorization"])
			assert.Equal(t, "test-integration-3", packageOwners["./internal/composition"])
			assert.Equal(t, "test-integration-3", packageOwners["./internal/gitcredentials"])
			assert.Equal(t, "test-integration-3", packageOwners["./internal/httpcredentials"])
			assert.Equal(t, "test-integration-2", packageOwners["./internal/mcpingress"])
		})
	}
}

func TestIntegrationShardExecutionPreservesOwnerBudget(t *testing.T) {
	root := filepath.Dir(purposeTargetModuleRoot(t))
	for _, shard := range []string{"test-integration-1", "test-integration-2", "test-integration-3"} {
		t.Run(shard, func(t *testing.T) {
			var sharedDeadline time.Time
			calls := 0
			executor := suiteExecutorFunc(func(ctx context.Context, _ string, command Command) ([]byte, error) {
				deadline, bounded := ctx.Deadline()
				require.True(t, bounded)
				require.Positive(t, time.Until(deadline))
				require.LessOrEqual(t, time.Until(deadline), 15*time.Minute)
				if calls == 0 {
					sharedDeadline = deadline
				}
				calls++
				assert.Equal(t, sharedDeadline, deadline)
				assert.Equal(t, 15*time.Minute, command.Timeout)
				return nil, nil
			})
			require.NoError(t, RunSuite(t.Context(), root, shard, 1, executor))
			require.Positive(t, calls)
		})
	}
}

func TestIntegrationInvocationPartitionsStaySequentialAndComplete(t *testing.T) {
	root := suiteFixture(t, map[string]string{
		"agent-gateway/internal/invocation/first_test.go": "package invocation\nimport \"testing\"\nfunc TestZulu(t *testing.T) {}\nfunc TestAlpha(t *testing.T) {}\nfunc TestMiddle(t *testing.T) {}\n",
		"agent-gateway/internal/invocation/new_test.go":   "package invocation\nimport \"testing\"\nfunc TestNew(t *testing.T) {}\nfunc TestBeta(t *testing.T) {}\n",
		"agent-gateway/internal/other/other_test.go":      "package other\nimport \"testing\"\nfunc TestOther(t *testing.T) {}\n",
		"agent-gateway/internal/storage/store_test.go":    "package storage\nimport \"testing\"\nfunc TestStorage(t *testing.T) {}\n",
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
	require.Len(t, commands[1].Tests, 3)
	assert.NotContains(t, commands[0].Argv, "./internal/storage")
	assert.Contains(t, commands[1].Argv, "./internal/storage")
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
			assert.Equal(t, 15*time.Minute, command.Timeout)
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

func TestIntegrationCompositionMatrixBatchesKeepOneOwnerBudget(t *testing.T) {
	root := suiteFixture(t, map[string]string{
		"agent-gateway/internal/composition/lifecycle_test.go": "package composition\nimport \"testing\"\nfunc TestAlpha(t *testing.T) {}\nfunc TestBeta(t *testing.T) {}\nfunc TestInitializationReconcilesCompletedEffectsWithoutDuplication(t *testing.T) {}\n",
		"agent-gateway/internal/invocation/lifecycle_test.go":  "package invocation\nimport \"testing\"\nfunc TestInvokeAlpha(t *testing.T) {}\nfunc TestInvokeBeta(t *testing.T) {}\n",
		"agent-gateway/internal/storage/lifecycle_test.go":     "package storage\nimport \"testing\"\nfunc TestStorage(t *testing.T) {}\n",
		"agent-gateway/internal/other/lifecycle_test.go":       "package other\nimport \"testing\"\nfunc TestOther(t *testing.T) {}\n",
	})
	for _, id := range []string{"test-integration", "test-integration-3"} {
		t.Run(id, func(t *testing.T) {
			for _, failAt := range []int{0, 1, 2, 3} {
				t.Run(fmt.Sprintf("failure-%d", failAt), func(t *testing.T) {
					calls := 0
					var sharedDeadline time.Time
					failure := errors.New("fixture composition batch failure")
					executor := suiteExecutorFunc(func(ctx context.Context, _ string, command Command) ([]byte, error) {
						calls++
						deadline, bounded := ctx.Deadline()
						require.True(t, bounded)
						require.Positive(t, time.Until(deadline))
						require.LessOrEqual(t, time.Until(deadline), 15*time.Minute)
						if calls == 1 {
							sharedDeadline = deadline
						}
						assert.Equal(t, sharedDeadline, deadline, "a third batch cannot renew the owner budget")
						assert.Equal(t, 15*time.Minute, command.Timeout)
						for _, flag := range []string{"-race", "-count=1", "-timeout=5m0s", "-tags=integration"} {
							assert.Contains(t, command.Arguments, flag)
						}
						if calls == failAt {
							return nil, failure
						}
						return nil, nil
					})
					err := RunSuite(t.Context(), root, id, 1, executor)
					if failAt == 0 {
						require.NoError(t, err)
						assert.Equal(t, 3, calls)
					} else {
						require.ErrorIs(t, err, failure)
						assert.Equal(t, failAt, calls, "failure stops every later batch")
					}
				})
			}
			for _, cancelAt := range []int{1, 2} {
				t.Run(fmt.Sprintf("cancel-%d", cancelAt), func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					calls := 0
					executor := suiteExecutorFunc(func(context.Context, string, Command) ([]byte, error) {
						calls++
						if calls == cancelAt {
							cancel()
						}
						return nil, nil
					})
					require.ErrorIs(t, RunSuite(ctx, root, id, 1, executor), context.Canceled)
					assert.Equal(t, cancelAt, calls, "cancellation stops every later batch")
				})
			}
		})
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
	_, err = PlanSuite(root, "test-integration-4", inventory, 1)
	require.Error(t, err)
	_, err = PlanSuite(root, "test-integration-1", inventory, 2)
	require.ErrorContains(t, err, "only stress")
}
