package gitpolicy

import (
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestGitPolicyClosedAndWriteRequiresRead(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"refs":[]}`, `{"version":1,"read":null,"refs":[]}`,
		`{"version":2,"read":true,"refs":[]}`, `{"version":1,"read":true,"refs":[],"http_default":"allow"}`,
		`{"version":1,"read":false,"refs":[{"ref":{"kind":"exact","value":"refs/heads/main"},"actions":["update"]}]}`,
		`{"version":1,"read":true,"read":false,"refs":[]}`,
	} {
		_, err := Decode([]byte(raw))
		require.Error(t, err, raw)
	}
}

func TestGitPolicyIndependentActionsAndUnorderedUnion(t *testing.T) {
	read := Grant{Policy: contract.GitPolicy{Version: 1, Read: true, Refs: []contract.GitRefRule{}}}
	write := Grant{Policy: contract.GitPolicy{Version: 1, Read: true, Refs: []contract.GitRefRule{{Ref: contract.GitRefSelector{Kind: "prefix", Value: "refs/heads/team/"}, Actions: []string{"create", "update"}}}}}
	cases := []struct {
		grants  []Grant
		actions []RefAction
		allowed bool
	}{
		{nil, nil, false}, {[]Grant{read}, nil, true},
		{[]Grant{read}, []RefAction{{"refs/heads/main", "update"}}, false},
		{[]Grant{write, read}, []RefAction{{"refs/heads/team/a", "create"}, {"refs/heads/team/b", "update"}}, true},
		{[]Grant{read, write}, []RefAction{{"refs/heads/team/a", "create"}, {"refs/heads/team/b", "delete"}}, false},
		{[]Grant{write}, []RefAction{{"refs/heads/teammate/a", "update"}}, false},
		{[]Grant{write}, []RefAction{{"refs/tags/team/a", "update"}}, false},
	}
	for _, c := range cases {
		allowed, err := Evaluate(c.grants, c.actions)
		require.NoError(t, err)
		require.Equal(t, c.allowed, allowed)
	}
	_, err := Evaluate([]Grant{read, {Policy: contract.GitPolicy{Version: 9}}}, nil)
	require.Error(t, err)
}

func TestGitRefsRejectUnsupportedForms(t *testing.T) {
	for _, ref := range []string{"main", "HEAD", "refs/heads/.hidden", "refs/heads/a.lock", "refs/heads/a..b", "refs/heads/a@{b", "refs/heads/a\\b", "refs/heads/a/", "refs/heads/a//b", "refs/heads/a*b"} {
		require.False(t, ValidRef(ref), ref)
	}
	require.True(t, ValidRef("refs/tags/v1.0"))
}

func TestGitLocatorAliasesCannotRetarget(t *testing.T) {
	canonical, err := Locator("https://GitHub.com/team/repo")
	require.NoError(t, err)
	require.Equal(t, "https://github.com:443/team/repo", canonical)
	aliases, err := Aliases(canonical, []string{"https://github.com/team/repo.git"})
	require.NoError(t, err)
	require.Equal(t, []string{"https://github.com:443/team/repo.git"}, aliases)
	for _, raw := range []string{"https://other.example/team/repo", "https://github.com/team/other", "https://github.com/team/repo/child"} {
		_, err := Aliases(canonical, []string{raw})
		require.Error(t, err)
	}
	for _, raw := range []string{"http://github.com/team/repo", "ssh://github.com/team/repo", "https://user@github.com/team/repo", "https://github.com/team/%72epo", "https://github.com/team/repo?", "https://github.com/team/repo/", "https://github.com/team/../repo"} {
		_, err := Locator(raw)
		require.Error(t, err, raw)
	}
	require.True(t, Overlaps(canonical, canonical+"/child"))
	require.False(t, Overlaps(canonical, canonical+"s"))
}
