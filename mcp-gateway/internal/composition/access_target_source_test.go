package composition

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccessTargetOwnership(t *testing.T) {
	root := gatewayModuleRoot(t)
	expected := map[string]string{
		"internal/authorization/types.go:CreateGrantRequest":               "Target",
		"internal/authorization/types.go:EvaluationRequest":                "Target",
		"internal/authorization/types.go:ResolvedVerification":             "Target",
		"internal/authorization/request_approval.go:ApprovalGrantMaterial": "Target",
		"internal/authorization/deny_conflicts.go:DenyConflictScope":       "Target",
		"internal/authorization/discovery_policy.go:StructuralGrant":       "Target",
		"internal/authorization/evaluator.go:evaluationGrant":              "target",
		"internal/authorization/self_projection.go:selfGrantRow":           "target",
	}
	for _, source := range productionSources(t, root) {
		assert.Empty(t, accessTargetOwnershipViolations(source), source.path)
		ast.Inspect(source.file, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok {
				return true
			}
			key := source.path + ":" + spec.Name.Name
			fieldName, expectedType := expected[key]
			if !expectedType {
				return true
			}
			structure, ok := spec.Type.(*ast.StructType)
			require.True(t, ok, key)
			found := false
			for _, field := range structure.Fields.List {
				for _, name := range field.Names {
					assert.NotContains(t, []string{"ServerID", "UpstreamName", "serverID", "upstreamName"}, name.Name, key)
					if name.Name == fieldName {
						selector, ok := field.Type.(*ast.SelectorExpr)
						require.True(t, ok, key)
						assert.Equal(t, "MCP", selector.Sel.Name, key)
						assert.Equal(t, "github.com/averycrespi/agent-tools/mcp-gateway/internal/accesstarget", source.selectorPackage(selector), key)
						found = true
					}
				}
			}
			assert.True(t, found, key)
			delete(expected, key)
			return true
		})
	}
	assert.Empty(t, expected, "all access seams must be inspected")
	policy := readProductionSource(t, root, "internal/grantrequests/policy.go")
	assert.NotContains(t, policy, "type ResolvedTarget")
	assert.Contains(t, policy, "func CanonicalDedupeIdentity(policy CompiledPolicy, target accesstarget.MCP)")
	assert.Contains(t, policy, "func ValidateNarrowing(submitted CompiledPolicy, submittedTarget accesstarget.MCP, approved CompiledPolicy, approvedTarget accesstarget.MCP)")
}

func accessTargetOwnershipViolations(source productionSource) []string {
	var violations []string
	for _, imported := range source.imports {
		if strings.HasPrefix(source.path, "internal/accesstarget/") &&
			(strings.Contains(imported, ".") || imported == "database/sql" || imported == "os" || strings.HasPrefix(imported, "os/") || imported == "net" || strings.HasPrefix(imported, "net/")) {
			violations = append(violations, fmt.Sprintf("%s: access target must not own authority, storage, or resolution: %s", source.path, imported))
		}
		if strings.HasPrefix(source.path, "internal/authorization/") {
			for _, owner := range []string{"catalog", "servers", "grantrequests", "selfservice", "invocation"} {
				if strings.HasSuffix(imported, "/internal/"+owner) {
					violations = append(violations, fmt.Sprintf("%s: authority must use supplied target facts, not import %s", source.path, imported))
				}
			}
		}
		if strings.HasPrefix(source.path, "internal/contract/") && strings.HasSuffix(imported, "/internal/accesstarget") {
			violations = append(violations, fmt.Sprintf("%s: public representations must remain independent of internal access targets", source.path))
		}
	}
	return violations
}

func TestAccessTargetOwnershipNegativeFixtures(t *testing.T) {
	for _, fixture := range []struct{ path, contents string }{
		{"internal/accesstarget/bad.go", "package accesstarget\nimport \"database/sql\"\nvar _ *sql.Tx\n"},
		{"internal/authorization/bad.go", "package authorization\nimport \"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog\"\nvar _ = catalog.SyntheticSnapshot\n"},
		{"internal/contract/bad.go", "package contract\nimport \"github.com/averycrespi/agent-tools/mcp-gateway/internal/accesstarget\"\nvar _ accesstarget.MCP\n"},
	} {
		violations := accessTargetOwnershipViolations(parseProductionFixture(t, fixture.path, fixture.contents))
		require.Len(t, violations, 1)
		assert.Contains(t, violations[0], fixture.path)
	}
}
