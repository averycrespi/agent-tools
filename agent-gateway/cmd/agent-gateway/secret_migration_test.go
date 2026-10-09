package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemovedSecretMaintenanceCommandsHaveNoAliasesOrCompletion(t *testing.T) {
	for _, operation := range []string{"migrate-secrets", "verify-secrets", "cleanup-native-secrets"} {
		t.Run(operation, func(t *testing.T) {
			command := newTestRootCmd(t)
			command.SetOut(new(bytes.Buffer))
			command.SetErr(new(bytes.Buffer))
			command.SetArgs([]string{"maintenance", operation, "--confirm"})
			require.Error(t, command.ExecuteContext(t.Context()))
			maintenance, _, err := newTestRootCmd(t).Find([]string{"maintenance"})
			require.NoError(t, err)
			for _, child := range maintenance.Commands() {
				require.NotEqual(t, operation, child.Name())
				require.NotContains(t, child.Aliases, operation)
			}
			completion := new(bytes.Buffer)
			require.NoError(t, newTestRootCmd(t).GenBashCompletionV2(completion, true))
			require.NotContains(t, completion.String(), operation)
		})
	}
}
