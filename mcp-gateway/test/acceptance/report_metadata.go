package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var commandDefinitionPaths = []string{
	"mcp-gateway/internal/testutil/cleanup_ledger.go",
	"mcp-gateway/internal/testutil/process_supervisor.go",
	"mcp-gateway/internal/testutil/runner.go",
	"mcp-gateway/test/acceptance/acceptance.go",
	"mcp-gateway/test/acceptance/report.schema.json",
	"mcp-gateway/test/keyringnative/result.schema.json",
}

type Adoption struct {
	SchemaVersion         int     `json:"schema_version"`
	ReportSHA256          string  `json:"report_sha256"`
	Revision              string  `json:"revision"`
	Profile               Profile `json:"profile"`
	ProfileHash           string  `json:"profile_hash"`
	CommandDefinitionHash string  `json:"command_definition_hash"`
	AdoptedAt             string  `json:"adopted_at"`
}

type profileDefinition struct {
	Profile  Profile   `json:"profile"`
	Commands []Command `json:"commands"`
}

type commandDefinitions struct {
	ProfileHash string           `json:"profile_hash"`
	Files       []definitionFile `json:"files"`
}

type definitionFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func ProfileDefinitionHash(profile Profile) (string, error) {
	commands, err := ProfileCommands(profile)
	if err != nil {
		return "", err
	}
	profileBytes, err := json.Marshal(profileDefinition{Profile: profile, Commands: commands})
	if err != nil {
		return "", fmt.Errorf("encode profile definition: %w", err)
	}
	profileSum := sha256.Sum256(profileBytes)
	return hex.EncodeToString(profileSum[:]), nil
}

func DefinitionHashes(root string, profile Profile) (string, string, error) {
	profileHash, err := ProfileDefinitionHash(profile)
	if err != nil {
		return "", "", err
	}
	definitions := commandDefinitions{ProfileHash: profileHash, Files: make([]definitionFile, 0, len(commandDefinitionPaths))}
	for _, relative := range commandDefinitionPaths {
		contents, err := os.ReadFile(filepath.Join(root, relative)) //nolint:gosec // Paths are closed constants below the caller-selected repository root.
		if err != nil {
			return "", "", fmt.Errorf("read command definition %s: %w", relative, err)
		}
		sum := sha256.Sum256(contents)
		definitions.Files = append(definitions.Files, definitionFile{Path: relative, SHA256: hex.EncodeToString(sum[:])})
	}
	definitionBytes, err := json.Marshal(definitions)
	if err != nil {
		return "", "", fmt.Errorf("encode command definitions: %w", err)
	}
	definitionSum := sha256.Sum256(definitionBytes)
	return profileHash, hex.EncodeToString(definitionSum[:]), nil
}

func ValidateReportDefinitions(root string, report Report) error {
	profileHash, definitionHash, err := DefinitionHashes(root, report.Profile)
	if err != nil {
		return err
	}
	if profileHash != report.ProfileHash {
		return errors.New("acceptance profile hash does not match current definitions")
	}
	if definitionHash != report.CommandDefinitionHash {
		return errors.New("acceptance command definition hash does not match current definitions")
	}
	return nil
}

func WriteReport(path string, report Report) error {
	contents, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode acceptance report: %w", err)
	}
	if _, err := Parse(contents); err != nil {
		return err
	}
	return writeAtomic(path, contents)
}

func AdoptReport(root, reportPath, outputPath string, now func() time.Time) (Adoption, error) {
	contents, err := os.ReadFile(reportPath) //nolint:gosec // The caller explicitly selects the immutable report to adopt.
	if err != nil {
		return Adoption{}, fmt.Errorf("read acceptance report: %w", err)
	}
	report, err := Parse(contents)
	if err != nil {
		return Adoption{}, err
	}
	if report.Result != ResultPassed || !report.CleanBefore || !report.CleanAfter || report.Dirty || report.DirtyPolicy != "required_clean" {
		return Adoption{}, errors.New("only a passed clean-revision report can be adopted")
	}
	gitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	revision, err := gitOutput(gitCtx, root, "rev-parse", "HEAD")
	if err != nil || revision != report.Revision {
		return Adoption{}, errors.New("acceptance report revision does not match current HEAD")
	}
	status, err := gitOutput(gitCtx, root, "status", "--porcelain")
	if err != nil || status != "" {
		return Adoption{}, errors.New("acceptance report adoption requires a clean workspace")
	}
	if err := ValidateReportDefinitions(root, report); err != nil {
		return Adoption{}, err
	}
	sum := sha256.Sum256(contents)
	adoption := Adoption{
		SchemaVersion: 1, ReportSHA256: hex.EncodeToString(sum[:]), Revision: report.Revision,
		Profile: report.Profile, ProfileHash: report.ProfileHash, CommandDefinitionHash: report.CommandDefinitionHash,
		AdoptedAt: now().UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(adoption)
	if err != nil {
		return Adoption{}, fmt.Errorf("encode report adoption: %w", err)
	}
	if err := writeAtomic(outputPath, encoded); err != nil {
		return Adoption{}, err
	}
	return adoption, nil
}

func writeAtomic(path string, contents []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".acceptance-*") //nolint:gosec // The caller owns the selected output directory.
	if err != nil {
		return fmt.Errorf("create atomic acceptance output: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure atomic acceptance output: %w", err)
	}
	if _, err := temporary.Write(append(contents, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write atomic acceptance output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync atomic acceptance output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close atomic acceptance output: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish atomic acceptance output: %w", err)
	}
	return nil
}
