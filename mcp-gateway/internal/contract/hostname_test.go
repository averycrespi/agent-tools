package contract

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHostnameGrammar(t *testing.T) {
	for _, host := range []string{"host.lima.internal", "LOCALHOST", "vm-2.example", "2vm", "xn--bcher-kva.example", strings.Repeat("a", 63) + ".example"} {
		normalized, ok := NormalizeHostname(host)
		assert.True(t, ok, host)
		assert.Equal(t, strings.ToLower(host), normalized)
	}
	for _, host := range []string{"", "http://host", "host:8210", "host/path", "*.example", "a..b", ".host", "host.", "-host", "host-", "a_b", "127.0.0.1", "127.1", "2130706433", "::1", "[host]", "host?x", "host#x", "user@host", " host", "host ", "host\n", "host\x00", "bücher.example", "K.example", strings.Repeat("a", 64), strings.Repeat("a.", 126) + "ab"} {
		_, ok := NormalizeHostname(host)
		assert.False(t, ok, "%q", host)
	}
}
