package credentials_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/credentials"
)

// TestEnvCredentialsCarryHostBinding is the AC-9 property that motivated
// collapsing the two reference forms into one: an environment-sourced
// credential is host-checked exactly as a keychain one is.
func TestEnvCredentialsCarryHostBinding(t *testing.T) {
	t.Setenv("GH_TOKEN", "SENTINEL-ENV")

	src := credentials.NewEnv(map[string]credentials.EnvSpec{
		"gh_bot": {Var: "GH_TOKEN", Hosts: []string{"api.github.com"}},
	})
	r := credentials.New(src)

	if _, err := r.Resolve("gh_bot", "api.github.com"); err != nil {
		t.Fatalf("Resolve on the bound host = %v, want it allowed", err)
	}

	_, err := r.Resolve("gh_bot", "api.stripe.com")
	if err == nil {
		t.Fatal("an env-sourced credential must be refused off its bound hosts")
	}
	if !errors.Is(err, credentials.ErrHostScope) {
		t.Fatalf("error %v, want it to wrap ErrHostScope", err)
	}
	if strings.Contains(err.Error(), "SENTINEL-ENV") {
		t.Errorf("the error must not contain the value: %q", err)
	}
}

func TestEnvMissingVariable(t *testing.T) {
	src := credentials.NewEnv(map[string]credentials.EnvSpec{
		"gh_bot": {Var: "DEFINITELY_NOT_SET_HTTP_BROKER", Hosts: []string{"api.github.com"}},
	})

	_, err := src.Get("gh_bot")
	if err == nil {
		t.Fatal("an unset variable should be an error")
	}
	if !errors.Is(err, credentials.ErrUnavailable) {
		t.Fatalf("error %v, want it to wrap ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "DEFINITELY_NOT_SET_HTTP_BROKER") {
		t.Errorf("error %q should name the variable so the operator can fix it", err)
	}
}

func TestEnvEmptyVariable(t *testing.T) {
	t.Setenv("EMPTY_TOKEN", "")

	src := credentials.NewEnv(map[string]credentials.EnvSpec{
		"gh_bot": {Var: "EMPTY_TOKEN", Hosts: []string{"api.github.com"}},
	})
	if _, err := src.Get("gh_bot"); err == nil {
		t.Fatal("a set-but-empty variable should be an error, not an empty credential")
	}
}

func TestEnvUnknownName(t *testing.T) {
	src := credentials.NewEnv(map[string]credentials.EnvSpec{})
	if _, err := src.Get("absent"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("error %v, want it to wrap ErrNotFound", err)
	}
}

func TestEnvNames(t *testing.T) {
	src := credentials.NewEnv(map[string]credentials.EnvSpec{
		"zeta":  {Var: "Z", Hosts: []string{"z.example.com"}},
		"alpha": {Var: "A", Hosts: []string{"a.example.com"}},
	})
	got := src.Names()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Errorf("Names() = %v, want [alpha zeta] sorted", got)
	}
}

// TestEnvDescribeHidesValue backs AC-8: `credential list` must never print a
// value.
func TestEnvDescribeHidesValue(t *testing.T) {
	t.Setenv("GH_TOKEN", "SENTINEL-VALUE")

	src := credentials.NewEnv(map[string]credentials.EnvSpec{
		"gh_bot": {Var: "GH_TOKEN", Hosts: []string{"API.GitHub.com"}},
	})
	meta, err := src.Describe("gh_bot")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	if meta.Name != "gh_bot" || meta.Source != "env_credentials" {
		t.Errorf("Describe = %+v, want the name and source", meta)
	}
	if len(meta.Hosts) != 1 || meta.Hosts[0] != "api.github.com" {
		t.Errorf("Hosts = %v, want the normalised bound host", meta.Hosts)
	}
	if meta.Bytes != len("SENTINEL-VALUE") {
		t.Errorf("Bytes = %d, want the value length", meta.Bytes)
	}

	// The whole struct must not carry the value anywhere.
	if strings.Contains(formatMeta(meta), "SENTINEL-VALUE") {
		t.Errorf("Metadata must not contain the value: %+v", meta)
	}
}

func formatMeta(m credentials.Metadata) string {
	return m.Name + "|" + m.Source + "|" + strings.Join(m.Hosts, ",")
}

func TestEnvRejectsUnboundSpec(t *testing.T) {
	t.Setenv("GH_TOKEN", "v")

	src := credentials.NewEnv(map[string]credentials.EnvSpec{
		"gh_bot": {Var: "GH_TOKEN"},
	})
	if _, err := src.Get("gh_bot"); err == nil {
		t.Fatal("an env credential with no bound hosts must not resolve")
	}
}
