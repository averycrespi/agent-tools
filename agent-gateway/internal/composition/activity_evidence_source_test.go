package composition

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActivityEvidenceOwnership(t *testing.T) {
	for _, source := range productionSources(t, gatewayModuleRoot(t)) {
		assert.Empty(t, activityEvidenceViolations(source), source.path)
	}
}

func activityEvidenceViolations(source productionSource) []string {
	var violations []string
	const activityPackage = "github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	for _, imported := range source.imports {
		if strings.HasPrefix(source.path, "internal/activity/") && imported != "github.com/averycrespi/agent-tools/agent-gateway/internal/contract" {
			violations = append(violations, fmt.Sprintf("%s: common evidence may import only closed contract values: %s", source.path, imported))
		}
		if (strings.HasPrefix(source.path, "internal/audit/") || strings.HasPrefix(source.path, "internal/contract/")) && imported == activityPackage {
			violations = append(violations, fmt.Sprintf("%s: administrative audit and public contracts remain independent of activity evidence", source.path))
		}
	}
	if strings.HasPrefix(source.path, "internal/activity/") {
		for _, declaration := range source.file.Decls {
			if _, ok := declaration.(*ast.FuncDecl); ok {
				violations = append(violations, fmt.Sprintf("%s: common evidence is value-only, not another runtime, authority, or writer", source.path))
			}
		}
		ast.Inspect(source.file, func(node ast.Node) bool {
			field, ok := node.(*ast.Field)
			if !ok {
				return true
			}
			for _, name := range field.Names {
				switch name.Name {
				case "Target", "Route", "ServerID", "ToolID", "UpstreamName", "RequestedName", "DescriptorRevision", "DescriptorFingerprint", "Arguments", "RedactedArguments":
					violations = append(violations, fmt.Sprintf("%s: MCP detail %s belongs to invocation", source.path, name.Name))
				}
			}
			return true
		})
	}
	return violations
}

func TestActivityEvidenceOwnershipNegativeFixtures(t *testing.T) {
	for _, fixture := range []struct{ path, contents string }{
		{"internal/activity/bad.go", "package activity\nimport \"database/sql\"\nvar _ *sql.Tx\n"},
		{"internal/activity/bad.go", "package activity\nfunc NewWriter() {}\n"},
		{"internal/activity/bad.go", "package activity\ntype Details struct { UpstreamName string }\n"},
		{"internal/audit/bad.go", "package audit\nimport \"github.com/averycrespi/agent-tools/agent-gateway/internal/activity\"\nvar _ activity.Envelope\n"},
		{"internal/contract/bad.go", "package contract\nimport \"github.com/averycrespi/agent-tools/agent-gateway/internal/activity\"\nvar _ activity.Envelope\n"},
	} {
		violations := activityEvidenceViolations(parseProductionFixture(t, fixture.path, fixture.contents))
		require.Len(t, violations, 1)
		assert.Contains(t, violations[0], fixture.path)
	}
}
