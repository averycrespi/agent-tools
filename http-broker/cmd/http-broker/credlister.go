package main

import (
	"fmt"
	"slices"
	"sort"

	"github.com/averycrespi/agent-tools/http-broker/internal/config"
	"github.com/averycrespi/agent-tools/http-broker/internal/credentials"
	"github.com/averycrespi/agent-tools/http-broker/internal/dashboard"
)

// credentialInfos builds the dashboard's credential rows.
//
// It is a pure function over its inputs so it can be tested without a keychain
// or a running stack, which is the only way the precedence rules below get
// covered at all — the keychain cannot be exercised in CI.
//
// The union of three name sets is deliberate. Indexed names are what is
// actually stored; referenced names are what the policy asks for, including
// ones that are missing; env keys are the config-sourced entries. A row
// present in only one of the three is exactly the case an operator is trying
// to diagnose.
func credentialInfos(
	indexed []string,
	referenced []string,
	env map[string]config.EnvCredential,
	describe func(name string) (credentials.Metadata, error),
) []dashboard.CredentialInfo {
	names := make([]string, 0, len(indexed)+len(referenced)+len(env))
	names = append(names, indexed...)
	names = append(names, referenced...)
	for name := range env {
		names = append(names, name)
	}
	slices.Sort(names)
	names = slices.Compact(names)

	infos := make([]dashboard.CredentialInfo, 0, len(names))
	for _, name := range names {
		ec, hasEnv := env[name]
		meta, err := describe(name)

		info := dashboard.CredentialInfo{
			Name:       name,
			Referenced: slices.Contains(referenced, name),
		}
		// The keychain-beats-env decision comes from credentials.SourceFor, so
		// the dashboard and the CLI cannot drift apart about which source a
		// request would actually use.
		switch source, _ := credentials.SourceFor(err == nil, hasEnv); source {
		case credentials.SourceKeychain:
			info.Source = source
			info.Hosts = meta.Hosts
		case credentials.SourceEnv:
			info.Source = source
			info.Hosts = ec.Hosts
		default:
			// Referenced but reachable from neither source. Shown rather than
			// omitted: a referenced-but-missing credential is the
			// misconfiguration that produces a 403, so hiding it would hide
			// the diagnosis.
			info.Source = "keychain (unavailable)"
		}
		infos = append(infos, info)
	}

	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// credentialListing wraps credentialInfos with the index read, carrying a read
// failure to the UI instead of silently shortening the list.
//
// A corrupt index must not take the dashboard down, but it must not quietly
// drop rows either: a credentials table that looks complete while missing
// entries is the worse failure. Reading and reporting is not a write, so this
// stays inside the read-only guarantee.
func credentialListing(
	index *credentials.Index,
	referenced []string,
	env map[string]config.EnvCredential,
	describe func(name string) (credentials.Metadata, error),
) dashboard.CredentialListing {
	indexed, err := index.Names()
	if err != nil {
		return dashboard.CredentialListing{
			Credentials: credentialInfos(nil, referenced, env, describe),
			// The index holds names only, so this can never carry a value.
			IndexError: fmt.Sprintf("could not read the credential index %s: %v", index.Path(), err),
		}
	}
	return dashboard.CredentialListing{Credentials: credentialInfos(indexed, referenced, env, describe)}
}
