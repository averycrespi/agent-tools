package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemovedSecretMaintenanceCommandsHaveNoAliasesOrCompletion(t *testing.T) {
	for _, operation := range []string{"migrate-secrets", "verify-secrets", "cleanup-native-secrets", "setup-secret-storage", "migrate-traffic-storage"} {
		t.Run(operation, func(t *testing.T) {
			command := newTestRootCmd(t)
			command.SetOut(new(bytes.Buffer))
			command.SetErr(new(bytes.Buffer))
			command.SetArgs([]string{"maintenance", operation, "--confirm"})
			require.Error(t, command.ExecuteContext(t.Context()))
			maintenance, _, err := newTestRootCmd(t).Find([]string{"maintenance"})
			require.NoError(t, err)
			require.Len(t, maintenance.Commands(), 2)
			for _, child := range maintenance.Commands() {
				require.NotEqual(t, operation, child.Name())
				require.NotContains(t, child.Aliases, operation)
			}
			help := new(bytes.Buffer)
			root := newTestRootCmd(t)
			root.SetOut(help)
			root.SetArgs([]string{"maintenance", "--help"})
			require.NoError(t, root.ExecuteContext(t.Context()))
			require.NotContains(t, help.String(), operation)
			completion := new(bytes.Buffer)
			require.NoError(t, newTestRootCmd(t).GenBashCompletionV2(completion, true))
			require.NotContains(t, completion.String(), operation)
			for _, generate := range []func(io.Writer) error{newTestRootCmd(t).GenZshCompletion, func(w io.Writer) error { return newTestRootCmd(t).GenFishCompletion(w, true) }, newTestRootCmd(t).GenPowerShellCompletion} {
				completion.Reset()
				require.NoError(t, generate(completion))
				require.NotContains(t, completion.String(), operation)
			}
		})
	}
}
