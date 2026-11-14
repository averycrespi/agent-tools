package configdefinition

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestDefinitionAndBundleCapacityBoundaries(t *testing.T) {
	for _, length := range []int{64, 65} {
		_, err := Normalize(definition("agent", strings.Repeat("a", length), agentSpec))
		if length == 64 {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrInvalid)
		}
	}
	b := contract.ConfigBundle{Format: contract.ConfigExportFormat, Version: 2, Resources: []contract.ConfigDefinition{definition("git_routing", "", `{"origins":[]}`)}}
	for i := 0; i < 128; i++ {
		b.Resources = append(b.Resources, definition("agent", fmt.Sprintf("agent-%d", i), agentSpec))
	}
	require.NoError(t, ValidateBundle(b))
	b.Resources = append(b.Resources, definition("agent", "one-too-many", agentSpec))
	require.ErrorIs(t, ValidateBundle(b), ErrInvalid)
}

func TestBundleRejectsUnresolvedGitAndDeletedCredentialBindings(t *testing.T) {
	base := contract.ConfigBundle{Format: contract.ConfigExportFormat, Version: 2, Resources: []contract.ConfigDefinition{definition("agent", "builder", agentSpec), definition("git_routing", "", `{"origins":[]}`)}}
	missing := definition("git_grant", "repo-access", `{"agent":{"kind":"agent","key":"builder"},"repository":{"kind":"git_repository","key":"missing"},"description":null,"policy":{"version":1,"read":true,"refs":[]},"expires_at":null}`)
	b := base
	b.Resources = append(append([]contract.ConfigDefinition{}, base.Resources...), missing)
	_, err := Normalize(missing)
	require.NoError(t, err)
	require.ErrorIs(t, ValidateBundle(b), ErrInvalid)
	credential := definition("git_credential", "api", `{"name":"API","origin":"https://github.com:443","recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":true,"secret_ref":null}`)
	repository := definition("git_repository", "repo", `{"name":"Repo","url":"https://github.com:443/org/repo.git","aliases":[],"credential":{"kind":"git_credential","key":"api"},"deleted":false}`)
	b = base
	b.Resources = append(append([]contract.ConfigDefinition{}, base.Resources...), credential, repository)
	_, err = Normalize(credential)
	require.NoError(t, err)
	_, err = Normalize(repository)
	require.NoError(t, err)
	require.ErrorIs(t, ValidateBundle(b), ErrInvalid)
	// An inactive target reservation may retain policies without inventing live
	// target configuration; it cannot act as credential authority.
	reserved := contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: 2, Kind: "git_repository", Key: "missing", Reserved: true, Spec: json.RawMessage("null")}
	b = base
	b.Resources = append(append([]contract.ConfigDefinition{}, base.Resources...), reserved, missing)
	require.NoError(t, ValidateBundle(b))
}
