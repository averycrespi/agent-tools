package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const (
	FrontendDevelopmentInventoryVersion = 1
	FrontendDevelopmentTarget           = "test-frontend-development"
	FrontendDevelopmentAggregatePhase   = "M4 combined checkpoint"
	FrontendDevelopmentAggregateRuns    = 1
	FrontendDevelopmentStructuralRuns   = 1
)

const FrontendDevelopmentBudget = 150 * time.Second

type FrontendDevelopmentLeaf struct {
	ID              string
	Command         string
	Timeout         time.Duration
	ExpectedMatches int
	GatewayStarts   int
	ChromiumStarts  int
}

type FrontendDevelopmentCriterionOwner struct {
	ID               string   `json:"id"`
	PrimaryOwner     string   `json:"primary_owner"`
	Command          string   `json:"command"`
	Selectors        []string `json:"selectors"`
	SupportingOwners []string `json:"supporting_owners,omitempty"`
}

type FrontendDevelopmentHandoffLeaf struct {
	ID                           string `json:"id"`
	Command                      string `json:"command"`
	TimeoutMilliseconds          int64  `json:"timeout_milliseconds"`
	MeasuredDurationMilliseconds int64  `json:"measured_duration_milliseconds"`
	ExpectedMatches              int    `json:"expected_matches"`
	GatewayStarts                int    `json:"gateway_starts"`
	ChromiumStarts               int    `json:"chromium_starts"`
	PreFinalRuns                 int    `json:"pre_final_runs"`
	FinalRuns                    int    `json:"final_runs"`
}

type FrontendDevelopmentHandoff struct {
	SchemaVersion              int                                 `json:"schema_version"`
	Namespace                  string                              `json:"namespace"`
	PlanPath                   string                              `json:"plan_path"`
	CheckpointSHA              string                              `json:"checkpoint_sha"`
	AggregateTarget            string                              `json:"aggregate_target"`
	AggregateCommand           string                              `json:"aggregate_command"`
	AggregateRuns              int                                 `json:"aggregate_runs"`
	CombinedCheckpointCommands []string                            `json:"combined_checkpoint_commands"`
	DefinitionFiles            []string                            `json:"definition_files"`
	Criteria                   []FrontendDevelopmentCriterionOwner `json:"criteria"`
	Leaves                     []FrontendDevelopmentHandoffLeaf    `json:"leaves"`
}

var FrontendDevelopmentLeaves = []FrontendDevelopmentLeaf{
	{
		ID:              "development.node-proxy-matrix",
		Command:         "npm --prefix .. run ui:test-dev",
		Timeout:         30 * time.Second,
		ExpectedMatches: 15,
	},
	{
		ID:              "development.browser-workflows",
		Command:         "go test -race -count=1 -tags=e2e,browser -timeout=2m -run '^TestFrontendDevelopment(LiveReload|ControlPlane)$' ./test/e2e",
		Timeout:         2 * time.Minute,
		ExpectedMatches: 2,
		GatewayStarts:   2,
		ChromiumStarts:  2,
	},
}

var frontendDevelopmentCriterionOwners = []FrontendDevelopmentCriterionOwner{
	{ID: "frontend.AC-1", PrimaryOwner: "development.node-proxy-matrix", Command: "npm run ui:test-dev", Selectors: []string{"selector|startup|source|contract projection"}, SupportingOwners: []string{"development.documentation"}},
	{ID: "frontend.AC-2", PrimaryOwner: "development.browser-live-reload", Command: "go -C mcp-gateway test -race -count=1 -tags=e2e,browser -timeout=2m -run '^TestFrontendDevelopmentLiveReload$' ./test/e2e", Selectors: []string{"TestFrontendDevelopmentLiveReload"}},
	{ID: "frontend.AC-3", PrimaryOwner: "development.browser-control-plane", Command: "go -C mcp-gateway test -race -count=1 -tags=e2e,browser -timeout=2m -run '^TestFrontendDevelopmentControlPlane$' ./test/e2e", Selectors: []string{"TestFrontendDevelopmentControlPlane"}},
	{ID: "frontend.AC-4", PrimaryOwner: "development.node-proxy-matrix", Command: "npm run ui:test-dev", Selectors: []string{"proxy admission", "proxy response", "proxy streaming", "proxy cancellation"}},
	{ID: "frontend.AC-5", PrimaryOwner: "development.node-proxy-matrix", Command: "npm run ui:test-dev", Selectors: []string{"proxy admission projects exact target, Origin, headers, and body once", "proxy admission preserves absent Origin"}, SupportingOwners: []string{"development.browser-control-plane"}},
	{ID: "frontend.AC-6", PrimaryOwner: "development.node-proxy-matrix", Command: "npm run ui:test-dev", Selectors: []string{"proxy streaming", "proxy cancellation", "proxy never replays a mutation"}, SupportingOwners: []string{"development.browser-control-plane"}},
	{ID: "frontend.AC-7", PrimaryOwner: "development.node-proxy-matrix", Command: "npm run ui:test-dev", Selectors: []string{"proxy and asset traffic leave canaries out of logs and temp state"}, SupportingOwners: []string{"development.browser-control-plane", "development.documentation"}},
	{ID: "frontend.AC-8", PrimaryOwner: "production.frontend-static", Command: "make -C mcp-gateway test-frontend-s6", Selectors: []string{"ui:verify-generated", "ui:verify-supply-chain", "TestStaticSupplyChain"}, SupportingOwners: []string{"production.browser-coordinator", "production.browser-workflows"}},
	{ID: "frontend.AC-9", PrimaryOwner: "development.documentation", Command: "go -C mcp-gateway test -race -count=1 -timeout=30s -run '^TestFrontendDevelopmentDocumentation$' ./test/acceptance", Selectors: []string{"TestFrontendDevelopmentDocumentation"}},
}

func FrontendDevelopmentHandoffHash(handoff FrontendDevelopmentHandoff) (string, error) {
	encoded, err := json.Marshal(handoff)
	if err != nil {
		return "", fmt.Errorf("encode frontend development handoff: %w", err)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)), nil
}

func NewFrontendDevelopmentHandoff(checkpointSHA string) (FrontendDevelopmentHandoff, error) {
	if len(checkpointSHA) != 40 {
		return FrontendDevelopmentHandoff{}, fmt.Errorf("checkpoint SHA must contain 40 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(checkpointSHA); err != nil {
		return FrontendDevelopmentHandoff{}, fmt.Errorf("checkpoint SHA must contain 40 lowercase hexadecimal characters")
	}
	for _, character := range checkpointSHA {
		if character >= 'A' && character <= 'F' {
			return FrontendDevelopmentHandoff{}, fmt.Errorf("checkpoint SHA must contain 40 lowercase hexadecimal characters")
		}
	}

	measured := map[string]time.Duration{
		"development.node-proxy-matrix": 4720 * time.Millisecond,
		"development.browser-workflows": 6880 * time.Millisecond,
	}
	leaves := make([]FrontendDevelopmentHandoffLeaf, 0, len(FrontendDevelopmentLeaves))
	for _, leaf := range FrontendDevelopmentLeaves {
		leaves = append(leaves, FrontendDevelopmentHandoffLeaf{
			ID: leaf.ID, Command: leaf.Command, TimeoutMilliseconds: leaf.Timeout.Milliseconds(),
			MeasuredDurationMilliseconds: measured[leaf.ID].Milliseconds(), ExpectedMatches: leaf.ExpectedMatches,
			GatewayStarts: leaf.GatewayStarts, ChromiumStarts: leaf.ChromiumStarts, PreFinalRuns: 1, FinalRuns: 1,
		})
	}
	criteria := make([]FrontendDevelopmentCriterionOwner, len(frontendDevelopmentCriterionOwners))
	for index, owner := range frontendDevelopmentCriterionOwners {
		criteria[index] = owner
		criteria[index].Selectors = append([]string(nil), owner.Selectors...)
		criteria[index].SupportingOwners = append([]string(nil), owner.SupportingOwners...)
	}
	return FrontendDevelopmentHandoff{
		SchemaVersion: 1, Namespace: "frontend", PlanPath: ".design/plans/2026-08-29-frontend-live-reload-development.md",
		CheckpointSHA: checkpointSHA, AggregateTarget: FrontendDevelopmentTarget,
		AggregateCommand: "make -C mcp-gateway test-frontend-development", AggregateRuns: FrontendDevelopmentAggregateRuns,
		CombinedCheckpointCommands: []string{"make -C mcp-gateway test-frontend-development", "make -C mcp-gateway test-cli-usability-e2e"},
		DefinitionFiles:            []string{"package.json", "mcp-gateway/Makefile", "mcp-gateway/test/acceptance/frontend_development_inventory.go", "mcp-gateway/web/dev-server.ts", "mcp-gateway/web/dev-proxy.ts", "mcp-gateway/test/e2e/frontend_development_test.go", "mcp-gateway/test/e2e/frontend_control_plane_test.go"},
		Criteria:                   criteria, Leaves: leaves,
	}, nil
}
