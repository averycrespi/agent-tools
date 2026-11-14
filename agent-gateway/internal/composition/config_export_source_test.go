package composition

import (
	"go/ast"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigurationExportNeverReadsMaterialOrRuntime(t *testing.T) {
	projections := map[string]bool{
		"internal/servers/config_export.go":         true,
		"internal/servers/config_references.go":     true,
		"internal/authorization/config_export.go":   true,
		"internal/httpcredentials/config_export.go": true,
		"internal/httpcredentials/config_v2.go":     true,
		"internal/gitcredentials/config_export.go":  true,
		"internal/gitcredentials/config_v2.go":      true,
		"internal/resourcekeys/config_export.go":    true,
	}
	found := 0
	for _, source := range productionSources(t, gatewayModuleRoot(t)) {
		projection := projections[source.path]
		if !projection && source.path != "internal/configexport/export.go" && source.path != "internal/configexport/v2.go" {
			continue
		}
		found++
		ast.Inspect(source.file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.BasicLit:
				literal, err := strconv.Unquote(value.Value)
				if projection && err == nil && strings.Contains(strings.ToUpper(literal), "SELECT") {
					for _, forbidden := range []string{"SELECT *", "credential_verifier", "credential_fingerprint", "material_revision", "handle", "secret_generations", "server_credentials", "server_oauth_registrations", "admin_credentials", "control_audit", "invocations", "grant_requests", "server_operations"} {
						require.NotContains(t, literal, forbidden, source.path)
					}
				}
			case *ast.ImportSpec:
				if projection {
					break
				}
				imported, err := strconv.Unquote(value.Path.Value)
				require.NoError(t, err)
				for _, forbidden := range []string{"/keyring", "/oauth", "/runtimes", "/remote", "/downstream", "/admin", "net/", "os"} {
					require.False(t, strings.HasPrefix(imported, forbidden) || strings.Contains(imported, "/internal"+forbidden), imported)
				}
			case *ast.CallExpr:
				if projection {
					break
				}
				if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
					owner := source.selectorPackage(selector)
					if strings.HasSuffix(owner, "/servers") || strings.HasSuffix(owner, "/authorization") || strings.HasSuffix(owner, "/httpcredentials") || strings.HasSuffix(owner, "/gitcredentials") {
						require.Contains(t, []string{"ConfigurationTx", "ConfigurationForKindTx", "ConfigurationReferencesTx", "RetainedConfigurationTx", "SortedConfiguration"}, selector.Sel.Name, "export may consume only configuration projections")
					}
				}
			}
			return true
		})
	}
	require.Equal(t, 10, found, "all explicit configuration projection owners must remain covered")
}
