package contract

type ClauseEvidence struct {
	Clause   string   `json:"clause"`
	Tasks    []string `json:"tasks"`
	Evidence []string `json:"evidence"`
}

var s4AcceptanceEvidenceManifest = []AcceptanceEvidence{
	{Criterion: "AC-1", Evidence: []string{"s4-contract", "s4-admission-race", "s4-ingress-wire", "s4-composition-race", "s4-e2e-call", "s4-source-guards", "s4-docs", "s4-acceptance", "s4-audit", "s4-vulnerability", "s4-repository-check"}},
	{Criterion: "AC-2", Evidence: []string{"s4-schema", "s4-admission-race", "s4-invocation-race", "s4-composition-race", "s4-repository-integration", "s4-e2e-call", "s4-source-guards", "s4-acceptance"}},
	{Criterion: "AC-3", Evidence: []string{"s4-catalog-race", "s4-downstream-race", "s4-invocation-race", "s4-lifecycle-race", "s4-composition-race", "s4-e2e-lifecycle", "s4-source-guards", "s4-acceptance"}},
	{Criterion: "AC-4", Evidence: []string{"s4-contract", "s4-schema", "s4-redaction-fuzz", "s4-sanitizer-fuzz", "s4-ingress-wire", "s4-invocation-race", "s4-e2e-privacy", "s4-source-privacy", "s4-docs", "s4-acceptance"}},
	{Criterion: "AC-5", Evidence: []string{"s4-contract", "s4-schema", "s4-repository-integration", "s4-invocation-race", "s4-e2e-privacy", "s4-docs", "s4-acceptance"}},
}

var s4ClauseEvidenceManifest = []ClauseEvidence{
	{Clause: "OUTCOME-1", Tasks: []string{"T9", "T17", "T18"}, Evidence: []string{"s4-invocation-race", "s4-e2e-call", "s4-e2e-lifecycle"}},
	{Clause: "SCOPE-1", Tasks: []string{"T9", "T11", "T20"}, Evidence: []string{"s4-invocation-race", "s4-ingress-wire", "s4-docs"}},
	{Clause: "NG-1", Tasks: []string{"T9", "T14", "T15", "T20"}, Evidence: []string{"s4-invocation-race", "s4-source-guards", "s4-docs"}},
	{Clause: "RB-1.1", Tasks: []string{"T11", "T12", "T13", "T17"}, Evidence: []string{"s4-ingress-wire", "s4-e2e-call"}},
	{Clause: "RB-1.2", Tasks: []string{"T11"}, Evidence: []string{"s4-ingress-wire"}},
	{Clause: "RB-1.3", Tasks: []string{"T4", "T9"}, Evidence: []string{"s4-catalog-race", "s4-invocation-race"}},
	{Clause: "RB-1.4", Tasks: []string{"T4", "T9"}, Evidence: []string{"s4-catalog-race", "s4-invocation-race"}},
	{Clause: "RB-1.5", Tasks: []string{"T3", "T19"}, Evidence: []string{"s4-redaction-fuzz", "s4-e2e-privacy"}},
	{Clause: "RB-1.6", Tasks: []string{"T1", "T3", "T19"}, Evidence: []string{"s4-contract", "s4-redaction-fuzz", "s4-e2e-privacy"}},
	{Clause: "RB-2.1", Tasks: []string{"T1", "T7", "T8"}, Evidence: []string{"s4-contract", "s4-repository-integration", "s4-admission-race"}},
	{Clause: "RB-2.2", Tasks: []string{"T1", "T7", "T8"}, Evidence: []string{"s4-contract", "s4-repository-integration", "s4-admission-race"}},
	{Clause: "RB-2.3", Tasks: []string{"T2", "T7", "T19"}, Evidence: []string{"s4-schema", "s4-repository-integration", "s4-e2e-privacy"}},
	{Clause: "RB-2.4", Tasks: []string{"T8"}, Evidence: []string{"s4-admission-race"}},
	{Clause: "RB-2.5", Tasks: []string{"T8", "T17"}, Evidence: []string{"s4-admission-race", "s4-e2e-call"}},
	{Clause: "RB-2.6", Tasks: []string{"T8", "T9"}, Evidence: []string{"s4-admission-race", "s4-invocation-race"}},
	{Clause: "RB-2.7", Tasks: []string{"T8", "T10"}, Evidence: []string{"s4-admission-race", "s4-lifecycle-race"}},
	{Clause: "RB-3.1", Tasks: []string{"T9", "T10"}, Evidence: []string{"s4-invocation-race", "s4-lifecycle-race"}},
	{Clause: "RB-3.2", Tasks: []string{"T9", "T10", "T18"}, Evidence: []string{"s4-invocation-race", "s4-lifecycle-race", "s4-e2e-lifecycle"}},
	{Clause: "RB-3.3", Tasks: []string{"T5", "T9", "T18"}, Evidence: []string{"s4-downstream-race", "s4-invocation-race", "s4-e2e-lifecycle"}},
	{Clause: "RB-3.4", Tasks: []string{"T2", "T7", "T9"}, Evidence: []string{"s4-schema", "s4-repository-integration", "s4-invocation-race"}},
	{Clause: "RB-3.5", Tasks: []string{"T7", "T20"}, Evidence: []string{"s4-repository-integration", "s4-docs"}},
	{Clause: "RB-4.1", Tasks: []string{"T1", "T11"}, Evidence: []string{"s4-contract", "s4-ingress-wire"}},
	{Clause: "RB-4.2", Tasks: []string{"T1", "T6", "T11", "T17"}, Evidence: []string{"s4-contract", "s4-sanitizer-fuzz", "s4-ingress-wire", "s4-e2e-call"}},
	{Clause: "RB-4.3", Tasks: []string{"T6", "T12", "T13"}, Evidence: []string{"s4-sanitizer-fuzz", "s4-ingress-wire"}},
	{Clause: "RB-4.4", Tasks: []string{"T5", "T6", "T19"}, Evidence: []string{"s4-downstream-race", "s4-sanitizer-fuzz", "s4-e2e-privacy"}},
	{Clause: "RB-4.5", Tasks: []string{"T6", "T9", "T19"}, Evidence: []string{"s4-sanitizer-fuzz", "s4-invocation-race", "s4-e2e-privacy"}},
	{Clause: "RB-5.1", Tasks: []string{"T1", "T2", "T7", "T19"}, Evidence: []string{"s4-contract", "s4-schema", "s4-repository-integration", "s4-e2e-privacy"}},
	{Clause: "RB-5.2", Tasks: []string{"T1", "T20"}, Evidence: []string{"s4-contract", "s4-docs"}},
	{Clause: "RB-5.3", Tasks: []string{"T7", "T19"}, Evidence: []string{"s4-repository-integration", "s4-e2e-privacy"}},
	{Clause: "DI-1", Tasks: []string{"T3", "T4", "T9"}, Evidence: []string{"s4-redaction-fuzz", "s4-catalog-race", "s4-invocation-race"}},
	{Clause: "DI-2", Tasks: []string{"T2", "T7"}, Evidence: []string{"s4-schema", "s4-repository-integration"}},
	{Clause: "DI-3", Tasks: []string{"T14", "T15", "T20"}, Evidence: []string{"s4-source-guards", "s4-docs"}},
	{Clause: "DI-4", Tasks: []string{"T1", "T2", "T7"}, Evidence: []string{"s4-contract", "s4-schema", "s4-repository-integration"}},
	{Clause: "FP-1", Tasks: []string{"T9", "T11"}, Evidence: []string{"s4-invocation-race", "s4-ingress-wire"}},
	{Clause: "FP-2", Tasks: []string{"T8", "T10"}, Evidence: []string{"s4-admission-race", "s4-lifecycle-race"}},
	{Clause: "FP-3", Tasks: []string{"T4", "T9", "T18"}, Evidence: []string{"s4-catalog-race", "s4-invocation-race", "s4-e2e-lifecycle"}},
	{Clause: "FP-4", Tasks: []string{"T5", "T6", "T18"}, Evidence: []string{"s4-downstream-race", "s4-sanitizer-fuzz", "s4-e2e-lifecycle"}},
	{Clause: "FP-5", Tasks: []string{"T8", "T9", "T17"}, Evidence: []string{"s4-admission-race", "s4-invocation-race", "s4-e2e-call"}},
	{Clause: "SO-1", Tasks: []string{"T7", "T20"}, Evidence: []string{"s4-repository-integration", "s4-docs"}},
	{Clause: "SO-2", Tasks: []string{"T3", "T6", "T19", "T20"}, Evidence: []string{"s4-redaction-fuzz", "s4-sanitizer-fuzz", "s4-e2e-privacy", "s4-docs"}},
	{Clause: "SO-3", Tasks: []string{"T9", "T10", "T15"}, Evidence: []string{"s4-invocation-race", "s4-lifecycle-race", "s4-source-guards"}},
	{Clause: "SO-4", Tasks: []string{"T16", "T19"}, Evidence: []string{"s4-source-privacy", "s4-e2e-privacy"}},
	{Clause: "COMP-1", Tasks: []string{"T12", "T13", "T17", "T18"}, Evidence: []string{"s4-ingress-wire", "s4-e2e-call", "s4-e2e-lifecycle"}},
	{Clause: "ARCH-1", Tasks: []string{"T9", "T14", "T15", "T20", "T21"}, Evidence: []string{"s4-invocation-race", "s4-composition-race", "s4-source-guards", "s4-docs", "s4-acceptance", "s4-audit", "s4-vulnerability", "s4-repository-check"}},
}

func S4AcceptanceEvidenceManifest() []AcceptanceEvidence {
	result := make([]AcceptanceEvidence, len(s4AcceptanceEvidenceManifest))
	for index, entry := range s4AcceptanceEvidenceManifest {
		result[index] = entry
		result[index].Evidence = append([]string(nil), entry.Evidence...)
	}
	return result
}

func S4ClauseEvidenceManifest() []ClauseEvidence {
	result := make([]ClauseEvidence, len(s4ClauseEvidenceManifest))
	for index, entry := range s4ClauseEvidenceManifest {
		result[index] = entry
		result[index].Tasks = append([]string(nil), entry.Tasks...)
		result[index].Evidence = append([]string(nil), entry.Evidence...)
	}
	return result
}
