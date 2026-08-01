package credentials

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"

	"github.com/averycrespi/agent-tools/http-broker/internal/paths"
)

// MaxKeychainValueBytes is go-keyring's macOS ceiling.
//
// Its macOS backend pipes the value to `security -i`, whose interactive parser
// truncates lines at roughly 4096 bytes. mcp-broker works around this by
// chunking; this tool does not (D3). An API key plus a short host list stays
// well under the limit, and chunking would add a partial-write failure mode to
// the one subsystem that must not have one. `credential set` fails clearly
// instead.
const MaxKeychainValueBytes = 4000

// envelope is what gets stored under one keychain item: the value together
// with its host binding, so the two can never be separated.
//
// Storing the hosts alongside the value is deliberate. If bindings lived in
// config.json instead, editing that file would silently re-scope a secret, and
// a credential copied to another machine would arrive unbound.
type envelope struct {
	Value string   `json:"value"`
	Hosts []string `json:"hosts"`
}

// Keychain is a Source backed by the OS keychain.
type Keychain struct {
	service string
}

// NewKeychain returns a Source reading from the OS keychain under the
// http-broker service name.
func NewKeychain() *Keychain { return &Keychain{service: paths.AppName} }

// Kind implements Source.
func (k *Keychain) Kind() string { return "keychain" }

// Get implements Source.
//
// A missing item is ErrNotFound. Any other failure is ErrUnavailable and is
// never downgraded to "not found": D4 makes an unreachable keychain a hard
// error rather than a silent fallback, because the alternative the reference
// implementation chose — falling back to a 0600 file — produces exactly the
// plaintext-secret-on-disk outcome this design exists to avoid.
func (k *Keychain) Get(name string) (Record, error) {
	raw, err := keyring.Get(k.service, name)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Record{}, fmt.Errorf("%w: %q", ErrNotFound, name)
		}
		return Record{}, fmt.Errorf("%w: reading %q from the keychain: %w%s",
			ErrUnavailable, name, err, keychainRemediation())
	}

	var env envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return Record{}, fmt.Errorf("%w: keychain item %q is not a valid http-broker envelope; re-run `http-broker credential set %s`",
			ErrUnavailable, name, name)
	}
	if env.Value == "" {
		return Record{}, fmt.Errorf("%w: keychain item %q holds an empty value", ErrUnavailable, name)
	}
	// envelope and Record hold the same fields; the conversion keeps them in
	// lockstep, since adding a field to one without the other stops compiling.
	return Record(env), nil
}

// Set stores a credential and its host binding.
func (k *Keychain) Set(name, value string, hosts []string) error {
	if !ValidName(name) {
		return fmt.Errorf("credential name %q must contain only letters, digits, and the characters . _ -", name)
	}
	if value == "" {
		return errors.New("credential value is empty")
	}
	if err := ValidateHeaderValue(value); err != nil {
		return err
	}

	normalized, err := NormalizeHosts(hosts)
	if err != nil {
		return err
	}

	data, err := json.Marshal(envelope{Value: value, Hosts: normalized})
	if err != nil {
		return fmt.Errorf("encoding credential envelope: %w", err)
	}
	if len(data) > MaxKeychainValueBytes {
		return fmt.Errorf("credential %q is %d bytes with its host list, over the %d-byte keychain limit; shorten the host list or use an env_credentials entry",
			name, len(data), MaxKeychainValueBytes)
	}

	if err := keyring.Set(k.service, name, string(data)); err != nil {
		return fmt.Errorf("%w: storing %q in the keychain: %w%s", ErrUnavailable, name, err, keychainRemediation())
	}
	return nil
}

// Delete removes a credential.
func (k *Keychain) Delete(name string) error {
	// Validate as Set does, so a malformed name produces the same clear error
	// rather than whatever the keychain backend happens to say.
	if !ValidName(name) {
		return fmt.Errorf("credential name %q must contain only letters, digits, and the characters . _ -", name)
	}
	if err := keyring.Delete(k.service, name); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return fmt.Errorf("%w: %q", ErrNotFound, name)
		}
		return fmt.Errorf("%w: deleting %q from the keychain: %w%s", ErrUnavailable, name, err, keychainRemediation())
	}
	return nil
}

// Metadata describes a stored credential without revealing its value.
type Metadata struct {
	Name   string   `json:"name"`
	Source string   `json:"source"`
	Hosts  []string `json:"hosts"`
	// Bytes is the length of the stored value, which is useful for spotting a
	// truncated or empty secret without printing it.
	Bytes int `json:"value_bytes"`
}

// Describe returns metadata for a stored credential. It deliberately returns
// no value: `credential list` must never print one (AC-8).
func (k *Keychain) Describe(name string) (Metadata, error) {
	record, err := k.Get(name)
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{Name: name, Source: k.Kind(), Hosts: record.Hosts, Bytes: len(record.Value)}, nil
}

// keychainRemediation appends actionable advice to a keychain failure.
//
// The headless case is the common one: go-keyring needs a Secret Service
// daemon on Linux, which CI and most Linux development hosts do not run.
func keychainRemediation() string {
	return "\n" +
		"  The OS keychain could not be reached. http-broker does not fall back to a file:\n" +
		"  a plaintext secret on disk is the outcome the keychain exists to avoid.\n" +
		"  On macOS, unlock the login keychain and grant access when prompted.\n" +
		"  On Linux or in CI, use an env_credentials entry in config.json instead —\n" +
		"  those carry the same host binding and go through the same checks."
}
