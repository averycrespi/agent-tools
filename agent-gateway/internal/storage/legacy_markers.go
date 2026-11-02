package storage

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

const markerBytesMaximum = 512

type markerDocument struct {
	InstallationID string          `json:"installation_id"`
	State          string          `json:"state"`
	Recovery       *recoveryAction `json:"recovery,omitempty"`
}
type recoveryAction struct {
	Action             string `json:"action"`
	Owner              string `json:"owner,omitempty"`
	Kind               string `json:"kind,omitempty"`
	PrincipalID        string `json:"principal_id,omitempty"`
	CredentialID       string `json:"credential_id,omitempty"`
	PrincipalRevision  int64  `json:"principal_revision,omitempty"`
	CredentialRevision int64  `json:"credential_revision,omitempty"`
}

type mutationMarker struct{ intent, temporary, tombstone string }

func newMutationMarker(layout gatewaypaths.Layout, _ func(FaultPoint) error) mutationMarker {
	return mutationMarker{intent: layout.MutationMarker, temporary: layout.MutationMarker + ".tmp", tombstone: layout.MutationMarker + ".cleared"}
}
func (marker mutationMarker) paths() []string {
	return []string{marker.intent, marker.temporary, marker.tombstone}
}
func (marker mutationMarker) hasArtifacts() (bool, error) {
	marked := false
	for _, path := range marker.paths() {
		info, err := os.Lstat(path)
		switch {
		case err == nil:
			if err := gatewaypaths.ValidateOwnerOnlyFile(path); err != nil {
				return false, err
			}
			if info.Size() > markerBytesMaximum {
				return false, fmt.Errorf("historical mutation marker is too large")
			}
			marked = true
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return false, err
		}
	}
	return marked, nil
}

// Recognize historical evidence only under validated installation ownership.
// These SQL-only actions are not compensation instructions: committed state is
// authoritative. Retain every original byte; unknown/conflicting evidence blocks.
func (marker mutationMarker) validateHistorical(installationID string) error {
	var prior *markerDocument
	for _, path := range marker.paths() {
		if err := gatewaypaths.ValidateOwnerOnlyFile(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, markerBytesMaximum+1))
		if err := errors.Join(readErr, file.Close()); err != nil {
			return err
		}
		var document markerDocument
		if err := strictjson.Decode(data, &document, strictjson.Options{MaxBytes: markerBytesMaximum, MaxDepth: 4, RejectUnknownMembers: true}); err != nil {
			return fmt.Errorf("invalid historical mutation evidence: %w", err)
		}
		if document.State != "armed" || document.InstallationID != installationID || document.Recovery != nil && !validRecoveryAction(*document.Recovery) {
			return fmt.Errorf("unrecognized or foreign historical mutation evidence")
		}
		if prior != nil && ((prior.Recovery == nil) != (document.Recovery == nil) || prior.Recovery != nil && *prior.Recovery != *document.Recovery) {
			return fmt.Errorf("historical mutation evidence conflicts")
		}
		prior = &document
	}
	return nil
}
func recoveryActionFromCandidate(candidate AgentCredentialCandidate) recoveryAction {
	return recoveryAction{Action: "invalidate_agent_credential_candidate", PrincipalID: candidate.PrincipalID, CredentialID: candidate.CredentialID, PrincipalRevision: candidate.PrincipalRevision, CredentialRevision: candidate.CredentialRevision}
}
func validRecoveryAction(recovery recoveryAction) bool {
	switch recovery.Action {
	case "restore_keyring_authority_fence":
		return validKeyringOwner(recovery.Owner) && validKeyringRecordKind(recovery.Kind) && recovery.PrincipalID == "" && recovery.CredentialID == "" && recovery.PrincipalRevision == 0 && recovery.CredentialRevision == 0
	case "invalidate_agent_credential_candidate":
		return recovery.Owner == "" && recovery.Kind == "" && validOpaqueID(recovery.PrincipalID) && validOpaqueID(recovery.CredentialID) && recovery.PrincipalRevision > 0 && recovery.PrincipalRevision < math.MaxInt64 && recovery.CredentialRevision > 0 && recovery.CredentialRevision < math.MaxInt64
	default:
		return false
	}
}
func validOpaqueID(value string) bool {
	return installationIDPattern.MatchString(value) && value[0] >= '0' && value[0] <= '7'
}
func validKeyringOwner(owner string) bool { return validOpaqueID(owner) }
func validKeyringRecordKind(kind string) bool {
	switch kind {
	case "static_credential", "oauth_client", "oauth_tokens", "http_credential", "http_ca", "git_credential":
		return true
	default:
		return false
	}
}
