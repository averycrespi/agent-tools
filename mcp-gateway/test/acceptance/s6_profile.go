package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const s6ExternalEvidenceDirectory = ".design/acceptance/s6/external"

var s6ExternalEvidenceFiles = []struct {
	Kind   string
	CellID string
	Name   string
}{
	{Kind: "real_safari", CellID: "macos-safari", Name: "real-safari.json"},
	{Kind: "manual_assistive", CellID: "macos-voiceover-safari", Name: "manual-assistive.json"},
}

type S6ExternalEvidenceReference struct {
	Kind         string `json:"kind"`
	CellID       string `json:"cell_id"`
	Availability string `json:"availability"`
	Result       string `json:"result"`
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
}

type s6CommandMetadata struct {
	Criteria  []string
	Evidence  []string
	Artifacts []string
	Native    bool
}

func s6ProfileCommands() []Command {
	all := []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6", "AC-7", "AC-8", "AC-9", "AC-10", "AC-11"}
	metadata := map[string]s6CommandMetadata{
		"format":                 {Criteria: []string{"AC-11"}, Evidence: []string{"documentation"}, Artifacts: []string{"package.json", "mcp-gateway/README.md"}},
		"gateway_verify":         {Criteria: []string{"AC-10", "AC-11"}, Evidence: []string{"validation-inventory"}, Artifacts: []string{"mcp-gateway/Makefile", "mcp-gateway/go.mod"}},
		"frontend":               {Criteria: []string{"AC-1", "AC-8", "AC-10"}, Evidence: []string{"frontend", "static-supply-chain"}, Artifacts: []string{"mcp-gateway/web", "mcp-gateway/internal/api/static"}},
		"go_unit":                {Criteria: all, Evidence: []string{"invocation-contract", "task"}, Artifacts: []string{"mcp-gateway/internal", "mcp-gateway/test/acceptance"}},
		"integration_compat":     {Criteria: []string{"AC-2", "AC-3", "AC-4", "AC-5", "AC-6", "AC-7", "AC-11"}, Evidence: []string{"invocation-integration", "integration-compat", "milestone-gate"}, Artifacts: []string{"mcp-gateway/internal/invocation", "mcp-gateway/internal/storage", "mcp-gateway/internal/backup"}},
		"browser_workflows":      {Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6", "AC-7", "AC-8", "AC-10"}, Evidence: []string{"browser-protocol", "browser-workflows"}, Artifacts: []string{"mcp-gateway/test/e2e", "mcp-gateway/web/tests/browser-coordinator.ts"}},
		"browser_visual":         {Criteria: []string{"AC-1", "AC-8"}, Evidence: []string{"browser-visual"}, Artifacts: []string{"mcp-gateway/web/tests/visual-matrix.ts"}},
		"browser_a11y":           {Criteria: []string{"AC-1", "AC-8"}, Evidence: []string{"browser-a11y"}, Artifacts: []string{"mcp-gateway/test/e2e/browser_accessibility_test.go"}},
		"browser_cross":          {Criteria: []string{"AC-2", "AC-8", "AC-10"}, Evidence: []string{"browser-cross", "external-evidence"}, Artifacts: []string{"mcp-gateway/web/environments.json", s6ExternalEvidenceDirectory}},
		"cli_e2e":                {Criteria: []string{"AC-3", "AC-4", "AC-5", "AC-6", "AC-7", "AC-9"}, Evidence: []string{"cli-e2e", "capability-matrix"}, Artifacts: []string{"mcp-gateway/test/e2e", "mcp-gateway/cmd/mcp-gateway"}},
		"security_privacy":       {Criteria: []string{"AC-2", "AC-5", "AC-6", "AC-10"}, Evidence: []string{"security-privacy", "privacy"}, Artifacts: []string{"mcp-gateway/test/security", "mcp-gateway/test/e2e/browser_secret_storage_privacy_test.go"}},
		"go_vulnerability":       {Criteria: []string{"AC-6", "AC-10"}, Artifacts: []string{"mcp-gateway/go.mod", "mcp-gateway/go.sum"}},
		"frontend_vulnerability": {Criteria: []string{"AC-6", "AC-10"}, Artifacts: []string{"package-lock.json"}},
		"native":                 {Criteria: []string{"AC-6", "AC-11"}, Artifacts: []string{"mcp-gateway/test/keyringnative/result.schema.json"}, Native: true},
		"other_tools":            {Criteria: []string{"AC-11"}, Artifacts: []string{"Makefile"}},
		"diff":                   {Criteria: []string{"AC-11"}, Evidence: []string{"accept-s6"}, Artifacts: []string{"README.md", "mcp-gateway/README.md", "mcp-gateway/DESIGN.md"}},
	}
	commands := make([]Command, 0, len(S6FinalCheckIDs))
	for _, owner := range S6FinalInventory() {
		leaf := owner.Leaves[0]
		definition := metadata[owner.ID]
		commands = append(commands, Command{
			CheckName: owner.ID,
			Name:      leaf.Argv[0],
			Arguments: append([]string(nil), leaf.Argv[1:]...),
			Criteria:  append([]string(nil), definition.Criteria...),
			Evidence:  append([]string(nil), definition.Evidence...),
			Artifacts: append([]string(nil), definition.Artifacts...),
			Native:    definition.Native,
			Timeout:   owner.Budget,
		})
	}
	return commands
}

func LoadS6ExternalEvidence(root string, binding S6EvidenceBinding) ([]S6ExternalEvidenceReference, error) {
	directory := filepath.Join(root, s6ExternalEvidenceDirectory)
	references := make([]S6ExternalEvidenceReference, 0, len(s6ExternalEvidenceFiles))
	for _, required := range s6ExternalEvidenceFiles {
		path := filepath.Join(directory, required.Name)
		contents, err := os.ReadFile(path) //nolint:gosec // The evidence filenames are closed constants under the selected repository root.
		if err != nil {
			return nil, fmt.Errorf("read %s external evidence: %w", required.Kind, err)
		}
		evidence, err := DecodeAndValidateS6ExternalEvidence(contents, directory, binding)
		if err != nil {
			return nil, fmt.Errorf("validate %s external evidence: %w", required.Kind, err)
		}
		if evidence.Kind != required.Kind || evidence.CellID != required.CellID {
			return nil, fmt.Errorf("external evidence %s has the wrong kind or cell", required.Name)
		}
		if evidence.Result == "failed" {
			return nil, fmt.Errorf("external evidence %s reports a blocking failure", required.Name)
		}
		digest := sha256.Sum256(contents)
		references = append(references, S6ExternalEvidenceReference{
			Kind: evidence.Kind, CellID: evidence.CellID, Availability: evidence.Availability, Result: evidence.Result,
			Path: filepath.ToSlash(filepath.Join(s6ExternalEvidenceDirectory, required.Name)), SHA256: "sha256:" + hex.EncodeToString(digest[:]),
		})
	}
	return references, nil
}

func QualifyS6ExternalEvidence(ctx context.Context, root string) ([]S6ExternalEvidenceReference, error) {
	revision, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return nil, errors.New("external qualification cannot resolve HEAD")
	}
	status, err := gitOutput(ctx, root, "status", "--porcelain")
	if err != nil || status != "" {
		return nil, errors.New("external qualification requires a clean workspace")
	}
	profileHash, err := ProfileDefinitionHash(ProfileS6)
	if err != nil {
		return nil, err
	}
	return LoadS6ExternalEvidence(root, S6EvidenceBinding{
		CandidateHead: revision, ManifestHash: S6InventoryDefinitionHash(), ProfileHash: "sha256:" + profileHash,
	})
}

func validateS6ExternalEvidenceReferences(references []S6ExternalEvidenceReference) error {
	if len(references) != len(s6ExternalEvidenceFiles) {
		return errors.New("S6 report requires every external evidence reference")
	}
	for index, reference := range references {
		expected := s6ExternalEvidenceFiles[index]
		if reference.Kind != expected.Kind || reference.CellID != expected.CellID || reference.Path != filepath.ToSlash(filepath.Join(s6ExternalEvidenceDirectory, expected.Name)) || !s6SHA256Pattern.MatchString(reference.SHA256) {
			return errors.New("S6 external evidence reference is not closed or digest-bound")
		}
		availablePassed := reference.Availability == "available" && reference.Result == "passed"
		additiveUnavailable := reference.Availability == "unavailable_additive" && reference.Result == "unavailable"
		if !availablePassed && !additiveUnavailable {
			return errors.New("S6 external evidence availability or result is invalid")
		}
	}
	return nil
}

func s6ProfileBudget() time.Duration {
	var total time.Duration
	for _, command := range s6ProfileCommands() {
		total += command.Timeout
	}
	return total
}
