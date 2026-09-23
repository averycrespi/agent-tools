package main

import (
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func validHTTPTrafficTarget(t *contract.HTTPTrafficTarget) bool {
	return t != nil && validHTTPResponseHost(t.Host) && !strings.Contains(t.Host, "*") && t.Port > 0 && ((t.Scheme == "" && t.Method == "") || ((t.Scheme == "http" || t.Scheme == "https") && validHTTPResponseMethod(t.Method)))
}
func validHTTPTrafficSummary(s contract.HTTPTrafficSummary) bool {
	if !contract.ValidAuditID(s.ID) || !contract.ValidAuditID(s.PrincipalID) {
		return false
	}
	if _, ok := httpResponseTime(s.AdmittedAt); !ok {
		return false
	}
	if !slices.Contains([]string{"allow", "block", "intercept", "invalid"}, s.Decision) || !slices.Contains([]string{"not_dispatched", "outcome_unknown", "succeeded", "prestart_failure", "upstream_failure"}, s.Outcome) {
		return false
	}
	if s.Type == "invalid" {
		return s.Target == nil && s.Decision == "invalid" && s.Outcome == "not_dispatched"
	}
	if !validHTTPTrafficTarget(s.Target) || s.Decision == "invalid" || (s.Decision != "allow" && s.Outcome != "not_dispatched") || (s.Decision == "allow" && s.Outcome == "not_dispatched") {
		return false
	}
	return (s.Type == "connect" && s.Target.Scheme == "") || (s.Type == "request" && s.Target.Scheme != "" && s.Decision != "intercept")
}
func validHTTPTrafficItem(item contract.HTTPTrafficRecord) bool {
	a := item.Admission
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > contract.HTTPTrafficAdmissionBytes {
		return false
	}
	admitted, ok := httpResponseTime(a.AdmittedAt)
	if !ok {
		return false
	}
	evaluated, ok := httpResponseTime(a.EvaluatedAt)
	if !ok || evaluated.Before(admitted) {
		return false
	}
	validRef := func(ref contract.HTTPRevisionRef) bool { return contract.ValidAuditID(ref.ID) && ref.Revision > 0 }
	fingerprint, err := hex.DecodeString(a.CredentialFingerprint)
	if !contract.ValidAuditID(a.ID) || !validRef(a.Principal) || !validRef(a.AgentCredential) || err != nil || len(fingerprint) != 8 || hex.EncodeToString(fingerprint) != a.CredentialFingerprint || a.Grants == nil || len(a.Grants) > contract.HTTPTrafficGrantFacts {
		return false
	}
	if a.Class == "invalid_request" {
		return a.Target == nil && a.Decision == nil && a.Default == "" && a.Material == nil && len(a.Grants) == 0 && item.Completion == nil
	}
	if a.Class != "evaluated" || !validHTTPTrafficTarget(a.Target) || a.Decision == nil || a.Decision.Principal != a.Principal || !validHTTPDecisionResponse(*a.Decision, a.Default) || (a.Target.Scheme != "") != (a.Decision.Transport == contract.HTTPTransportRequest) {
		return false
	}
	d := a.Decision
	expected := map[string]contract.HTTPRevisionRef{}
	for _, ref := range []*contract.HTTPRevisionRef{d.Grant, d.PrivateGrant, d.CredentialGrant, d.ConflictGrant} {
		if ref != nil {
			if prior, exists := expected[ref.ID]; exists && prior != *ref {
				return false
			}
			expected[ref.ID] = *ref
		}
	}
	if len(expected) != len(a.Grants) {
		return false
	}
	previous := ""
	for _, grant := range a.Grants {
		if grant.Reference.ID <= previous || expected[grant.Reference.ID] != grant.Reference {
			return false
		}
		previous = grant.Reference.ID
		policy, err := json.Marshal(grant.Policy)
		if err != nil || !validHTTPPolicyResponse(policy) {
			return false
		}
	}
	if a.Material != nil && (!d.Allowed || d.Credential == nil || a.Material.Credential != *d.Credential || !validCanonicalRevision(a.Material.Generation) || a.Material.Generation == "0") {
		return false
	}
	if d.Allowed && d.Credential != nil && a.Material == nil {
		return false
	}
	c := item.Completion
	if c == nil {
		return true
	}
	completed, ok := httpResponseTime(c.CompletedAt)
	if !d.Allowed || !ok || completed.Before(evaluated) || c.BytesSent < 0 || c.BytesReceived < 0 || c.DurationMS < 0 || !slices.Contains([]string{"succeeded", "prestart_failure", "upstream_failure", "outcome_unknown"}, c.Outcome) {
		return false
	}
	if c.Status != 0 && (d.Transport != contract.HTTPTransportRequest || c.Status < 100 || c.Status > 599) {
		return false
	}
	return c.Outcome != "prestart_failure" || (c.Status == 0 && c.BytesSent == 0 && c.BytesReceived == 0)
}
