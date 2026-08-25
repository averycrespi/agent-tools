package contract

type AcceptanceEvidence struct {
	Criterion string
	Evidence  []string
}

var acceptanceEvidenceManifest = []AcceptanceEvidence{
	{Criterion: "AC-1", Evidence: []string{"contract", "authorization-race", "api-wire", "mcp-wire", "restore-canary", "e2e-lifecycle", "source-secret", "audit"}},
	{Criterion: "AC-2", Evidence: []string{"contract", "authorization-race", "api-wire", "e2e-discovery", "source-slice", "audit"}},
	{Criterion: "AC-3", Evidence: []string{"contract", "strictjson-generated", "authorization-fuzz", "discovery-race", "audit"}},
	{Criterion: "AC-4", Evidence: []string{"authorization-race", "authorization-integration", "discovery-race", "ingress-race", "composition-race", "e2e-lifecycle", "audit"}},
	{Criterion: "AC-5", Evidence: []string{"discovery-race", "ingress-race", "mcp-wire", "e2e-discovery", "source-slice", "audit"}},
	{Criterion: "AC-6", Evidence: []string{"contract", "migration-restore", "integration", "e2e", "source-secret", "source-slice", "docs", "audit", "vulnerability", "native", "repository-check"}},
}

func AcceptanceEvidenceManifest() []AcceptanceEvidence {
	result := make([]AcceptanceEvidence, len(acceptanceEvidenceManifest))
	for index, entry := range acceptanceEvidenceManifest {
		result[index] = entry
		result[index].Evidence = append([]string(nil), entry.Evidence...)
	}
	return result
}
