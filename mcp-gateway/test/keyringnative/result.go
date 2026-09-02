package keyringnative

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	ResultPassed  = "passed"
	ResultSkipped = "skipped"
	ResultFailed  = "failed"
)

type Evidence struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Command string `json:"command"`
}

type Result struct {
	SchemaVersion int        `json:"schema_version"`
	Result        string     `json:"result"`
	Platform      string     `json:"platform"`
	Reason        string     `json:"reason"`
	Evidence      []Evidence `json:"evidence"`
}

//go:embed result.schema.json
var resultSchema []byte

func NewResult(result, platform, reason, deterministicStatus, nativeStatus string) Result {
	return Result{SchemaVersion: 1, Result: result, Platform: platform, Reason: reason, Evidence: []Evidence{
		{Name: "deterministic_material_composition", Status: deterministicStatus, Command: "go test -race ./test/material"},
		{Name: "native_backend", Status: nativeStatus, Command: "go test -race -tags=keyringnative ./internal/keyring ./test/material"},
	}}
}

func Parse(contents []byte) (Result, error) {
	var result Result
	if err := strictjson.Decode(contents, &result, strictjson.Options{MaxBytes: 4096, MaxDepth: 5, RejectUnknownMembers: true}); err != nil {
		return Result{}, err
	}
	var schemaDocument any
	if err := json.Unmarshal(resultSchema, &schemaDocument); err != nil {
		return Result{}, fmt.Errorf("decode native evidence schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:mcp-gateway:keyring-native-result", schemaDocument); err != nil {
		return Result{}, fmt.Errorf("load native evidence schema: %w", err)
	}
	schema, err := compiler.Compile("urn:mcp-gateway:keyring-native-result")
	if err != nil {
		return Result{}, fmt.Errorf("compile native evidence schema: %w", err)
	}
	var instance any
	if err := json.Unmarshal(contents, &instance); err != nil {
		return Result{}, err
	}
	if err := schema.Validate(instance); err != nil {
		return Result{}, fmt.Errorf("validate native evidence schema: %w", err)
	}
	if err := validateClassification(result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func validateClassification(result Result) error {
	deterministic, native := result.Evidence[0].Status, result.Evidence[1].Status
	switch result.Result {
	case ResultPassed:
		if deterministic != ResultPassed || native != ResultPassed {
			return errors.New("passed native result requires all evidence to pass")
		}
	case ResultSkipped:
		if deterministic != ResultPassed || native != ResultSkipped {
			return errors.New("skipped native result requires passed deterministic and skipped native evidence")
		}
	case ResultFailed:
		if !strings.EqualFold(deterministic, ResultFailed) && !strings.EqualFold(native, ResultFailed) {
			return errors.New("failed native result requires failed evidence")
		}
	default:
		return errors.New("unknown native result")
	}
	return nil
}
