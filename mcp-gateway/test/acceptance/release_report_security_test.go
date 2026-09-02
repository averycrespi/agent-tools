//go:build security

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type releaseSecurityExecutor struct {
	canary string
}

func (executor releaseSecurityExecutor) Run(context.Context, string, Command) ([]byte, error) {
	return []byte(executor.canary), errors.New(executor.canary)
}

func TestReleaseReportSecretSinkBoundaries(t *testing.T) {
	const canary = "release-report-output-canary-7f31289c"
	root, definition, external := releaseRunnerFixture(t)
	report, err := runReleaseProfile(t.Context(), root, releaseSecurityExecutor{canary: canary}, definition, external, passedReleaseCleanup)
	require.NoError(t, err)
	require.Equal(t, ResultFailed, report.Result)
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), canary)
	for _, forbidden := range []string{`"stdout"`, `"stderr"`, `"error"`, `"output"`} {
		assert.NotContains(t, string(encoded), forbidden)
	}
}
