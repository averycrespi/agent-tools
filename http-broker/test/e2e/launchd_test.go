//go:build e2e

package e2e_test

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plistPath is the LaunchAgent example the README and docs/launchd.md tell
// operators to copy into ~/Library/LaunchAgents.
func plistPath() string {
	return filepath.Join(mustFindModuleRoot(), "http-broker",
		"examples", "launchd", "http-broker.plist")
}

// TestLaunchdPlistIsWellFormed is a regression test.
//
// A comment containing a double hyphen is illegal XML, and `--no-open` and
// `--host` both read naturally in prose about this file. The example shipped
// malformed: nothing parses it in CI, and launchd only complains at
// `launchctl bootstrap`, by which point the operator is debugging their own
// machine rather than this repository.
func TestLaunchdPlistIsWellFormed(t *testing.T) {
	f, err := os.Open(plistPath())
	if err != nil {
		t.Fatalf("opening the plist: %v", err)
	}
	defer func() { _ = f.Close() }()

	dec := xml.NewDecoder(f)
	// The plist DOCTYPE references an external DTD that must not be fetched.
	dec.Strict = true
	dec.Entity = xml.HTMLEntity
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("%s is not well-formed XML: %v", plistPath(), err)
		}
	}
}

// TestLaunchdPlistRunsServeWithoutOpening pins the arguments the example
// passes. Opening a browser at every login is wrong for a supervised daemon,
// and the flag is easy to lose when the file is edited by hand.
func TestLaunchdPlistRunsServeWithoutOpening(t *testing.T) {
	data, err := os.ReadFile(plistPath())
	if err != nil {
		t.Fatalf("reading the plist: %v", err)
	}

	var doc struct {
		Strings []string `xml:"dict>array>string"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing the plist: %v", err)
	}

	joined := strings.Join(doc.Strings, " ")
	for _, want := range []string{"http-broker", "serve", "--no-open"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ProgramArguments %q is missing %q", joined, want)
		}
	}
}
