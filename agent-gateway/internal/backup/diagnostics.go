package backup

import "github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"

type backupCause struct {
	phase string
	err   error
}

func (cause *backupCause) Error() string            { return cause.phase + ": " + cause.err.Error() }
func (cause *backupCause) Unwrap() error            { return cause.err }
func withBackupCause(phase string, err error) error { return &backupCause{phase: phase, err: err} }

func artifactPredicate(rule, facts string) error {
	return diagnostics.WithDetail(ErrInvalidArtifact, diagnostics.Detail{Component: "backup", Operation: "verify artifact", Explanation: "rule=" + rule + " " + facts})
}

func observeInventory(observer diagnostics.HTTPProxyObserver, path string, err error) {
	if observer == nil {
		return
	}
	detail := diagnostics.Snapshot("backup", "load inventory", path, err)
	detail.Effect = "inventory unavailable; preserve artifacts and inspect path"
	observer.HTTPProxy(diagnostics.Facts{Event: diagnostics.OperatorFailure, Detail: detail})
}
