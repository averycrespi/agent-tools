package contract

import (
	"reflect"
	"strings"
	"testing"
)

func TestOAuthCallbackURI(t *testing.T) {
	for raw, bind := range map[string]string{
		"http://localhost:3118/callback":       "127.0.0.1:3118",
		"http://127.0.0.1:8210/oauth/callback": "127.0.0.1:8210",
		"http://[::1]:3118/callback":           "[::1]:3118",
	} {
		parsed, actual, err := ParseOAuthCallbackURI(raw)
		if err != nil || actual != bind || parsed.String() != raw {
			t.Fatalf("callback %q: %v %q", raw, err, actual)
		}
	}
	for _, raw := range []string{
		"", "https://localhost:3118/callback", "http://localhost/callback", "http://localhost:0/callback",
		"http://localhost:65536/callback", "http://localhost:03118/callback", "http://localhost:3118",
		"http://user@localhost:3118/callback", "http://localhost:3118/callback?", "http://localhost:3118/callback#",
		"http://localhost:3118/callback?x=1", "http://localhost:3118/callback#x", "http://example.test:3118/callback",
		"http://0.0.0.0:3118/callback", "http://[::]:3118/callback", "http://localhost.:3118/callback",
		"http://LOCALHOST:3118/callback", "http://localhost:3118/a/../callback", "http://localhost:3118//callback",
		"http://localhost:3118/%63allback", "http://[::ffff:127.0.0.1]:3118/callback", "http://127.1:3118/callback",
		"http://localhost:3118/call\\back", "http://[::1%25lo]:3118/callback",
	} {
		if _, _, err := ParseOAuthCallbackURI(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestNormalizeOAuthScopes(t *testing.T) {
	original := []string{"z", "a", "z"}
	got, err := NormalizeOAuthScopes(original)
	if err != nil || !reflect.DeepEqual(got, []string{"a", "z"}) || original[0] != "z" {
		t.Fatalf("normalization: %v %v", got, err)
	}
	empty, err := NormalizeOAuthScopes(nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal("empty scope set lost")
	}
	for _, values := range [][]string{{""}, {"a b"}, {"a\tb"}, {"a\"b"}, {"a\\b"}, {"é"}, {strings.Repeat("a", 257)}, make([]string, 65)} {
		if _, err := NormalizeOAuthScopes(values); err == nil {
			t.Errorf("accepted invalid scopes %q", values)
		}
	}
}
