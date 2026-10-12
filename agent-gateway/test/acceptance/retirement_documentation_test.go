package acceptance

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func TestRetirementTypeSafeGrantMatchesCurrentPolicy(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(repositoryRoot(t), "agent-gateway/docs/operators/http-proxy.md"))
	require.NoError(t, err)
	section := strings.SplitN(string(contents), "## TypeSafe API and SDK migration", 2)
	require.Len(t, section, 2)
	blocks := regexp.MustCompile("(?s)```json\\n(.*?)\\n```").FindAllStringSubmatch(strings.SplitN(section[1], "\n## ", 2)[0], -1)
	require.Len(t, blocks, 1)
	const principal = "00000000000000000000000001"
	const credential = "00000000000000000000000002"
	var file struct {
		PrincipalID string          `json:"principal_id"`
		Policy      json.RawMessage `json:"policy"`
	}
	raw := strings.NewReplacer("PRINCIPAL_ID", principal, "CREDENTIAL_ID", credential).Replace(blocks[0][1])
	require.NoError(t, json.Unmarshal([]byte(raw), &file))
	require.Equal(t, principal, file.PrincipalID)
	policy, err := httppolicy.DecodePolicy(file.Policy)
	require.NoError(t, err)
	evaluator, err := httppolicy.New(httppolicy.Snapshot{
		Principal: contract.HTTPRevisionRef{ID: principal, Revision: 1}, PolicyRevision: 1, DefaultRevision: 1, Default: contract.HTTPDefaultBlock,
		Grants:      []httppolicy.Grant{{Ref: contract.HTTPRevisionRef{ID: "00000000000000000000000003", Revision: 1}, PrincipalID: principal, Policy: policy}},
		Credentials: []httppolicy.Credential{{Ref: contract.HTTPRevisionRef{ID: credential, Revision: 1}, Available: true, Origins: []contract.HTTPOriginSelector{{Scheme: "https", Host: "api.typesafe.ai", Port: 443}}}},
	})
	require.NoError(t, err)
	for _, test := range []struct {
		method, url string
		allowed     bool
	}{
		{"POST", "https://api.typesafe.ai/v1/systemone", true},
		{"GET", "https://api.typesafe.ai/v1/systemone", false},
		{"POST", "https://api.typesafe.ai/v1/systemone/extra", false},
		{"GET", "https://api.typesafe.ai/v1/models", false},
		{"POST", "https://other.example/v1/systemone", false},
		{"POST", "http://api.typesafe.ai/v1/systemone", false},
	} {
		t.Run(test.method+" "+test.url, func(t *testing.T) {
			target, err := url.Parse(test.url)
			require.NoError(t, err)
			request, err := httppolicy.ParseRequest(test.url, test.method, target.Host, "", nil)
			require.NoError(t, err)
			decision, err := evaluator.RequestPolicy(request)
			require.NoError(t, err)
			require.Equal(t, test.allowed, decision.Allowed)
			if test.allowed {
				require.NotNil(t, decision.Credential)
				require.Equal(t, credential, decision.Credential.ID)
			}
		})
	}
}

func TestRetirementHistoryAndActiveModuleInventory(t *testing.T) {
	root := repositoryRoot(t)
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	require.NoError(t, err)
	assertMarkdownLinksResolve(t, filepath.Join(root, "README.md"), string(readme))
	workspace, err := os.ReadFile(filepath.Join(root, "go.work"))
	require.NoError(t, err)
	for _, tool := range []string{"local-git-mcp", "typesafe-mcp"} {
		_, err := os.Lstat(filepath.Join(root, tool))
		require.True(t, os.IsNotExist(err), "retired source must not remain active: %s", tool)
		require.NotContains(t, string(workspace), "./"+tool)
		require.Contains(t, string(readme), "https://github.com/averycrespi/agent-tools/tree/33b2f302864a5439a893b3f701e0a77ece5738d7/"+tool)
	}
}
