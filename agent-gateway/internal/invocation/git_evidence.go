package invocation

import (
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

const gitTrafficChargeBase int64 = 1024 + contract.GitTrafficCompletionBytes
const gitTrafficSelect = `SELECT insertion_sequence,id,admission,completion,bytes FROM git_traffic`

func validGitRef(ref contract.GitRevisionRef) bool {
	return validOpaqueInvocationID(ref.ID) && gitpolicy.ValidRevision(ref.Revision)
}
func encodeGitAdmission(a contract.GitTrafficAdmission) (string, error) {
	admitted, ok := parseCanonicalInvocationTimestamp(a.AdmittedAt)
	evaluated, valid := parseCanonicalInvocationTimestamp(a.EvaluatedAt)
	if !ok || !valid || evaluated.Before(admitted) || !validOpaqueInvocationID(a.ID) || !validGitRef(a.Principal) || !validGitRef(a.AgentCredential) || !gitpolicy.ValidRevision(a.ProfileRevision) || !gitpolicy.ValidRevision(a.AuthorizationRevision) || a.Commands < 0 || a.Commands > contract.GitRequestedRefs {
		return "", ErrInvalidInput
	}
	if a.Denial != "" && (a.Denial != "credential_unavailable" || a.Allowed || a.Rejection != "") {
		return "", ErrInvalidInput
	}
	if a.Rejection != "" {
		if !slices.Contains([]string{"unsupported", "repository_unavailable", "destination_unavailable"}, a.Rejection) || a.Allowed || a.Operation != "invalid" || a.Commands != 0 || a.Repository != (contract.GitRevisionRef{}) || a.AliasRevision != "" || a.Material != nil || a.PrivateGrant != nil {
			return "", ErrInvalidInput
		}
	} else if !validGitRef(a.Repository) || !gitpolicy.ValidRevision(a.AliasRevision) {
		return "", ErrInvalidInput
	}
	if !slices.Contains([]string{"read_discovery", "read", "push_discovery", "probe", "push", "invalid"}, a.Operation) || (a.Operation == "push") != (a.Commands > 0) || (a.Operation == "invalid") != (a.Rejection != "") {
		return "", ErrInvalidInput
	}
	if p := a.Policy; p != nil {
		canonical, err := gitpolicy.Locator(p.RepositoryURL)
		if err != nil || canonical != p.RepositoryURL || len(p.RepositoryName) == 0 || len(p.RepositoryName) > 256 || p.Grants == nil || len(p.Grants) > 4 || p.GrantCount < len(p.Grants) || p.GrantCount > contract.GitGrants || p.Creates < 0 || p.Updates < 0 || p.Deletes < 0 || p.Creates+p.Updates+p.Deletes != a.Commands || a.Rejection != "" {
			return "", ErrInvalidInput
		}
		seen := map[string]bool{}
		for _, ref := range p.Grants {
			if !validGitRef(ref) || seen[ref.ID] {
				return "", ErrInvalidInput
			}
			seen[ref.ID] = true
		}
	}
	if a.Material != nil && (!a.Allowed || !validGitRef(a.Material.Credential) || !gitpolicy.ValidRevision(a.Material.Generation)) {
		return "", ErrInvalidInput
	}
	if a.PrivateGrant != nil && !validHTTPRef(*a.PrivateGrant) {
		return "", ErrInvalidInput
	}
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > contract.GitTrafficAdmissionBytes {
		return "", ErrInvalidInput
	}
	return string(raw), nil
}
func encodeGitCompletion(a contract.GitTrafficAdmission, c contract.GitTrafficCompletion) (string, error) {
	completed, ok := parseCanonicalInvocationTimestamp(c.CompletedAt)
	evaluated, valid := parseCanonicalInvocationTimestamp(a.EvaluatedAt)
	if !ok || !valid || completed.Before(evaluated) || !a.Allowed || c.BytesSent < 0 || c.BytesReceived < 0 || c.DurationMS < 0 || c.Status != 0 && (c.Status < 100 || c.Status > 599) {
		return "", ErrInvalidInput
	}
	if !slices.Contains([]string{"", "credential_unavailable", "authorization_unavailable"}, c.Failure) || c.Failure != "" && c.Outcome != "prestart_failure" {
		return "", ErrInvalidInput
	}
	if !slices.Contains([]string{"", "reported_success", "reported_failure", "reported_partial"}, c.ReportedResult) || c.ReportedResult != "" && (a.Operation != "push" || !c.TransferComplete || c.Status != 200 || c.Outcome != "outcome_unknown") {
		return "", ErrInvalidInput
	}
	if !slices.Contains([]string{"prestart_failure", "outcome_unknown", "nonmutation"}, c.Outcome) {
		return "", ErrInvalidInput
	}
	if c.Outcome == "prestart_failure" && (c.Status != 0 || c.BytesSent != 0 || c.BytesReceived != 0 || c.TransferComplete) {
		return "", ErrInvalidInput
	}
	if c.TransferComplete && c.Status == 0 || c.Outcome == "nonmutation" && (!c.TransferComplete || a.Operation == "push") {
		return "", ErrInvalidInput
	}
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > contract.GitTrafficCompletionBytes {
		return "", ErrInvalidInput
	}
	return string(raw), nil
}
func scanGitTraffic(scanner invocationScanner) (contract.GitTrafficRecord, int64, error) {
	var r contract.GitTrafficRecord
	var id, admission string
	var completion sql.NullString
	var charge int64
	if err := scanner.Scan(&r.Sequence, &id, &admission, &completion, &charge); err != nil {
		return r, 0, err
	}
	if strictjson.Decode([]byte(admission), &r.Admission, strictjson.Options{MaxBytes: contract.GitTrafficAdmissionBytes, MaxDepth: 4, RejectUnknownMembers: true}) != nil {
		return r, 0, ErrInvalidState
	}
	canonical, err := encodeGitAdmission(r.Admission)
	if err != nil || canonical != admission || id != r.Admission.ID || charge != gitTrafficChargeBase+int64(len(admission)) {
		return r, 0, ErrInvalidState
	}
	if completion.Valid {
		r.Completion = &contract.GitTrafficCompletion{}
		if strictjson.Decode([]byte(completion.String), r.Completion, strictjson.Options{MaxBytes: contract.GitTrafficCompletionBytes, MaxDepth: 2, RejectUnknownMembers: true}) != nil {
			return r, 0, ErrInvalidState
		}
		canonical, err = encodeGitCompletion(r.Admission, *r.Completion)
		if err != nil || canonical != completion.String {
			return r, 0, ErrInvalidState
		}
	}
	return r, charge, nil
}
