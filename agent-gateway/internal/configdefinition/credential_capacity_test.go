package configdefinition

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func credentialDefinition(t *testing.T, kind string, index int, deleted bool) contract.ConfigDefinition {
	t.Helper()
	key := fmt.Sprintf("credential-%d", index)
	var secret *string
	if !deleted {
		value := kind + "/" + key + "/secret"
		secret = &value
	}
	var spec any
	if kind == "git_credential" {
		spec = contract.ConfigGitCredentialSpec{GitCredentialDefinition: contract.GitCredentialDefinition{Name: "Git", Origin: "https://github.com:443", Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, Deleted: deleted, SecretRef: secret}
	} else {
		spec = contract.ConfigHTTPCredentialSpec{HTTPCredentialDefinition: contract.HTTPCredentialDefinition{Name: "HTTP", Boundary: contract.HTTPCredentialBoundary{Host: "example.com", Port: 443}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, Deleted: deleted, SecretRef: secret}
	}
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	return definition(kind, key, string(raw))
}

func TestBundleRetainedCredentialCapacitySeparatesRowsAndReservations(t *testing.T) {
	for _, kind := range []string{"git_credential", "http_credential"} {
		t.Run(kind, func(t *testing.T) {
			for _, active := range []int{0, 1, 256} {
				t.Run(fmt.Sprintf("active-%d", active), func(t *testing.T) {
					b := contract.ConfigBundle{Format: contract.ConfigExportFormat, Version: 2, Resources: []contract.ConfigDefinition{definition("git_routing", "", `{"origins":[]}`)}}
					for i := 0; i < 1024; i++ {
						b.Resources = append(b.Resources, credentialDefinition(t, kind, i, i >= active))
					}
					require.NoError(t, ValidateBundle(b))
					reservation := definition(kind, "key-only-reservation", "null")
					reservation.Reserved = true
					b.Resources = append(b.Resources, reservation)
					require.NoError(t, ValidateBundle(b), "key-only reservations do not consume retained row capacity")
					b.Resources = append(b.Resources, credentialDefinition(t, kind, 1024, true))
					require.ErrorIs(t, ValidateBundle(b), ErrInvalid, "1025 retained definitions exceed native identity capacity")
				})
			}
			b := contract.ConfigBundle{Format: contract.ConfigExportFormat, Version: 2, Resources: []contract.ConfigDefinition{definition("git_routing", "", `{"origins":[]}`)}}
			for i := 0; i < 257; i++ {
				b.Resources = append(b.Resources, credentialDefinition(t, kind, i, false))
			}
			require.ErrorIs(t, ValidateBundle(b), ErrInvalid, "active capacity remains256 independently of row capacity")
		})
	}
}
