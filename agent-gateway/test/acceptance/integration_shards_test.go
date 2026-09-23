package acceptance

import (
	"testing"

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
