package contract

import "strconv"

var s5AcceptanceEvidenceManifest = []AcceptanceEvidence{
	{Criterion: "AC-1", Evidence: []string{"s5-contract", "s5-unit", "s5-e2e", "s5-security", "s5-source-guards", "s5-acceptance"}},
	{Criterion: "AC-2", Evidence: []string{"s5-contract", "s5-unit", "s5-integration", "s5-stress", "s5-e2e", "s5-acceptance"}},
	{Criterion: "AC-3", Evidence: []string{"s5-contract", "s5-unit", "s5-integration", "s5-e2e", "s5-acceptance"}},
	{Criterion: "AC-4", Evidence: []string{"s5-contract", "s5-unit", "s5-integration", "s5-stress", "s5-e2e", "s5-acceptance"}},
	{Criterion: "AC-5", Evidence: []string{"s5-unit", "s5-integration", "s5-stress", "s5-e2e", "s5-acceptance"}},
	{Criterion: "AC-6", Evidence: []string{"s5-unit", "s5-integration", "s5-e2e", "s5-security", "s5-source-guards", "s5-docs", "s5-vulnerability", "s5-native", "s5-other-tools", "s5-acceptance"}},
	{Criterion: "AC-7", Evidence: []string{"s5-contract", "s5-unit", "s5-integration", "s5-stress", "s5-e2e", "s5-security", "s5-docs", "s5-vulnerability", "s5-native", "s5-other-tools", "s5-acceptance"}},
}

type s5ClauseSection struct {
	Prefix   string
	Count    int
	Tasks    []string
	Evidence []string
}

var s5ClauseSections = []s5ClauseSection{
	{Prefix: "CATALOG", Count: 5, Tasks: []string{"T3", "T10", "T13", "T17", "T20"}, Evidence: []string{"s5-contract", "s5-unit", "s5-e2e", "s5-source-guards", "s5-docs"}},
	{Prefix: "DESCRIPTOR", Count: 3, Tasks: []string{"T3", "T10", "T12"}, Evidence: []string{"s5-contract", "s5-unit", "s5-e2e"}},
	{Prefix: "INVOCATION", Count: 6, Tasks: []string{"T11", "T12", "T13", "T15", "T17", "T18", "T25"}, Evidence: []string{"s5-unit", "s5-integration", "s5-stress", "s5-e2e"}},
	{Prefix: "REQUEST", Count: 5, Tasks: []string{"T5", "T6", "T7", "T8", "T22", "T23"}, Evidence: []string{"s5-contract", "s5-unit", "s5-integration"}},
	{Prefix: "EVIDENCE", Count: 6, Tasks: []string{"T4", "T5", "T6", "T8", "T9", "T14", "T18"}, Evidence: []string{"s5-contract", "s5-unit", "s5-integration", "s5-e2e", "s5-security"}},
	{Prefix: "DEDUPLICATION", Count: 5, Tasks: []string{"T5", "T6", "T12", "T17", "T18"}, Evidence: []string{"s5-unit", "s5-integration", "s5-stress", "s5-e2e"}},
	{Prefix: "LIFECYCLE", Count: 8, Tasks: []string{"T4", "T6", "T8", "T14", "T18", "T22", "T23", "T24"}, Evidence: []string{"s5-contract", "s5-unit", "s5-integration", "s5-e2e"}},
	{Prefix: "DENY", Count: 6, Tasks: []string{"T5", "T6", "T8", "T15", "T17"}, Evidence: []string{"s5-unit", "s5-integration", "s5-stress", "s5-e2e"}},
	{Prefix: "ADJUDICATION", Count: 7, Tasks: []string{"T8", "T9", "T15", "T17"}, Evidence: []string{"s5-contract", "s5-unit", "s5-integration", "s5-stress", "s5-e2e"}},
	{Prefix: "STATE", Count: 4, Tasks: []string{"T3", "T8", "T23"}, Evidence: []string{"s5-contract", "s5-unit", "s5-integration"}},
	{Prefix: "REPRESENTATION", Count: 9, Tasks: []string{"T3", "T7", "T9", "T22", "T24"}, Evidence: []string{"s5-contract", "s5-unit", "s5-e2e"}},
	{Prefix: "TOOL", Count: 8, Tasks: []string{"T3", "T12", "T17"}, Evidence: []string{"s5-contract", "s5-unit", "s5-e2e"}},
	{Prefix: "API", Count: 8, Tasks: []string{"T3", "T9", "T17"}, Evidence: []string{"s5-contract", "s5-unit", "s5-e2e"}},
	{Prefix: "OPERATIONS", Count: 4, Tasks: []string{"T3", "T13", "T24"}, Evidence: []string{"s5-contract", "s5-integration", "s5-e2e"}},
	{Prefix: "FAILURE", Count: 9, Tasks: []string{"T6", "T8", "T14", "T18", "T25"}, Evidence: []string{"s5-unit", "s5-integration", "s5-e2e", "s5-security"}},
	{Prefix: "SECURITY", Count: 7, Tasks: []string{"T1", "T2", "T13", "T16", "T18", "T20", "T21", "T26"}, Evidence: []string{"s5-security", "s5-source-guards", "s5-docs", "s5-vulnerability", "s5-native"}},
	{Prefix: "COMPATIBILITY", Count: 4, Tasks: []string{"T4", "T14", "T15", "T18", "T20"}, Evidence: []string{"s5-integration", "s5-e2e", "s5-other-tools", "s5-docs"}},
	{Prefix: "ARCHITECTURE", Count: 2, Tasks: []string{"T3", "T13", "T19", "T20", "T26", "T27"}, Evidence: []string{"s5-contract", "s5-source-guards", "s5-docs", "s5-acceptance"}},
}

func S5AcceptanceEvidenceManifest() []AcceptanceEvidence {
	result := make([]AcceptanceEvidence, len(s5AcceptanceEvidenceManifest))
	for index, entry := range s5AcceptanceEvidenceManifest {
		result[index] = entry
		result[index].Evidence = append([]string(nil), entry.Evidence...)
	}
	return result
}

func S5ClauseEvidenceManifest() []ClauseEvidence {
	result := make([]ClauseEvidence, 0, len(requiredS5ClauseIDs()))
	for _, section := range s5ClauseSections {
		for clause := 1; clause <= section.Count; clause++ {
			result = append(result, ClauseEvidence{
				Clause: section.Prefix + "-" + strconv.Itoa(clause),
				Tasks:  append([]string(nil), section.Tasks...), Evidence: append([]string(nil), section.Evidence...),
			})
		}
	}
	return result
}

func requiredS5ClauseIDs() []string {
	result := make([]string, 0, 106)
	for _, section := range s5ClauseSections {
		for clause := 1; clause <= section.Count; clause++ {
			result = append(result, section.Prefix+"-"+strconv.Itoa(clause))
		}
	}
	return result
}
