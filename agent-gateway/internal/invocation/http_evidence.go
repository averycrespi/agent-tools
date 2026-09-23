package invocation

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"math"
	"slices"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

const httpTrafficChargeBase int64 = 1024 + contract.HTTPTrafficCompletionBytes

func validHTTPRef(r contract.HTTPRevisionRef) bool {
	return validOpaqueInvocationID(r.ID) && r.Revision > 0 && r.Revision <= math.MaxInt64
}

func encodeHTTPAdmission(a contract.HTTPTrafficAdmission) (string, error) {
	if !validHTTPAdmission(a) {
		return "", ErrInvalidInput
	}
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > contract.HTTPTrafficAdmissionBytes {
		return "", ErrInvalidInput
	}
	return string(raw), nil
}

func validHTTPAdmission(a contract.HTTPTrafficAdmission) bool {
	admitted, ok := parseCanonicalInvocationTimestamp(a.AdmittedAt)
	evaluated, valid := parseCanonicalInvocationTimestamp(a.EvaluatedAt)
	if !ok || !valid || evaluated.Before(admitted) || !validOpaqueInvocationID(a.ID) || !validHTTPRef(a.Principal) || !validHTTPRef(a.AgentCredential) || !validFingerprint(a.CredentialFingerprint, 16) || a.Grants == nil || len(a.Grants) > contract.HTTPTrafficGrantFacts {
		return false
	}
	if a.Class == "invalid_request" {
		return a.Default == "" && a.Target == nil && a.Decision == nil && len(a.Grants) == 0 && a.Material == nil
	}
	if a.Class != "evaluated" || a.Target == nil || a.Decision == nil || (a.Default != contract.HTTPDefaultAllow && a.Default != contract.HTTPDefaultBlock) {
		return false
	}
	t := a.Target
	destination, err := httppolicy.NewDestination(t.Host, t.Port)
	if err != nil || destination.Host() != t.Host {
		return false
	}
	if t.Scheme == "" {
		if t.Method != "" {
			return false
		}
	} else {
		if _, err := httppolicy.ParseRequest(t.Scheme+"://"+destination.Authority()+"/", t.Method, destination.Authority(), "", nil); err != nil {
			return false
		}
	}
	d := a.Decision
	if d.Version != 1 || d.Principal != a.Principal || d.PolicyRevision == 0 || d.PolicyRevision > math.MaxInt64 || d.DefaultRevision == 0 || d.DefaultRevision > math.MaxInt64 {
		return false
	}
	if !validHTTPTrafficDecision(*d, a.Default, t.Scheme != "") {
		return false
	}
	refs := []*contract.HTTPRevisionRef{d.Grant, d.PrivateGrant, d.CredentialGrant, d.ConflictGrant}
	expected := make(map[string]contract.HTTPRevisionRef, 4)
	for _, ref := range refs {
		if ref != nil {
			if !validHTTPRef(*ref) {
				return false
			}
			if old, exists := expected[ref.ID]; exists && old != *ref {
				return false
			}
			expected[ref.ID] = *ref
		}
	}
	if len(expected) != len(a.Grants) {
		return false
	}
	previous := ""
	for _, g := range a.Grants {
		if expected[g.Reference.ID] != g.Reference || g.Reference.ID <= previous {
			return false
		}
		previous = g.Reference.ID
		p, err := httppolicy.Compile(g.Policy)
		if err != nil {
			return false
		}
		canonical, err := p.JSON()
		raw, marshalErr := json.Marshal(g.Policy)
		if err != nil || marshalErr != nil || !bytes.Equal(canonical, raw) {
			return false
		}
	}
	for _, ref := range []*contract.HTTPRevisionRef{d.Credential, d.ConflictCredential} {
		if ref != nil && !validHTTPRef(*ref) {
			return false
		}
	}
	if a.Material != nil && (!d.Allowed || d.Credential == nil || a.Material.Credential != *d.Credential || !validPositiveRevision(a.Material.Generation)) {
		return false
	}
	return !d.Allowed || d.Credential == nil || a.Material != nil
}

func encodeHTTPCompletion(a contract.HTTPTrafficAdmission, c contract.HTTPTrafficCompletion) (string, error) {
	completed, ok := parseCanonicalInvocationTimestamp(c.CompletedAt)
	evaluated, valid := parseCanonicalInvocationTimestamp(a.EvaluatedAt)
	if !ok || !valid || completed.Before(evaluated) || a.Decision == nil || !a.Decision.Allowed || c.BytesSent < 0 || c.BytesReceived < 0 || c.DurationMS < 0 {
		return "", ErrInvalidInput
	}
	if !slices.Contains([]string{"succeeded", "prestart_failure", "upstream_failure", "outcome_unknown"}, c.Outcome) {
		return "", ErrInvalidInput
	}
	if c.Status != 0 && (c.Status < 100 || c.Status > 599 || a.Decision.Transport != contract.HTTPTransportRequest) {
		return "", ErrInvalidInput
	}
	if c.Outcome == "prestart_failure" && (c.Status != 0 || c.BytesSent != 0 || c.BytesReceived != 0) {
		return "", ErrInvalidInput
	}
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > contract.HTTPTrafficCompletionBytes {
		return "", ErrInvalidInput
	}
	return string(raw), nil
}

func scanHTTPTraffic(scanner invocationScanner) (contract.HTTPTrafficRecord, int64, error) {
	var r contract.HTTPTrafficRecord
	var id, admission string
	var completion sql.NullString
	var charge int64
	if err := scanner.Scan(&r.Sequence, &id, &admission, &completion, &charge); err != nil {
		return r, 0, err
	}
	if strictjson.Decode([]byte(admission), &r.Admission, strictjson.Options{MaxBytes: contract.HTTPTrafficAdmissionBytes, MaxDepth: 12, RejectUnknownMembers: true}) != nil {
		return r, 0, ErrInvalidState
	}
	canonical, err := encodeHTTPAdmission(r.Admission)
	if err != nil || canonical != admission || id != r.Admission.ID || charge != httpTrafficChargeBase+int64(len(admission)) {
		return r, 0, ErrInvalidState
	}
	if completion.Valid {
		r.Completion = &contract.HTTPTrafficCompletion{}
		if strictjson.Decode([]byte(completion.String), r.Completion, strictjson.Options{MaxBytes: contract.HTTPTrafficCompletionBytes, MaxDepth: 2, RejectUnknownMembers: true}) != nil {
			return r, 0, ErrInvalidState
		}
		canonical, err := encodeHTTPCompletion(r.Admission, *r.Completion)
		if err != nil || canonical != completion.String {
			return r, 0, ErrInvalidState
		}
	}
	return r, charge, nil
}

const httpTrafficSelect = `SELECT insertion_sequence,id,admission,completion,bytes FROM http_traffic`
