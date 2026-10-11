package gitwire

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
)

func statusPacket(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
func TestStatusObserver(t *testing.T) {
	good := statusPacket("unpack ok\n") + statusPacket("ok refs/heads/a\n") + statusPacket("ok refs/heads/b\n") + "0000"
	partial := statusPacket("unpack ok\n") + statusPacket("ok refs/heads/a\n") + statusPacket("ng refs/heads/b private rejection message\n") + "0000"
	failed := statusPacket("unpack ok\n") + statusPacket("ng refs/heads/a denied\n") + statusPacket("ng refs/heads/b denied\n") + "0000"
	for _, tc := range []struct {
		name, wire, want string
		sideband         bool
	}{
		{"success", good, "reported_success", false},
		{"partial", partial, "reported_partial", false},
		{"failure", failed, "reported_failure", false},
		{"sideband", statusPacket("\x02private progress") + statusPacket("\x01"+good[:7]) + statusPacket("\x01"+good[7:]) + "0000", "reported_success", true},
		{"missing", "", "unknown", false},
		{"truncated", good[:len(good)-1], "unknown", false},
		{"partial report", statusPacket("unpack ok\n") + statusPacket("ok refs/heads/a\n") + "0000", "unknown", false},
		{"foreign ref", strings.Replace(good, "heads/b", "heads/c", 1), "unknown", false},
		{"duplicate", strings.Replace(good, "heads/b", "heads/a", 1), "unknown", false},
		{"trailing", good + "0000", "unknown", false},
		{"sideband fatal", statusPacket("\x03private error") + "0000", "unknown", true},
		{"unterminated inner", statusPacket("\x01"+good[:len(good)-4]) + "0000", "unknown", true},
		{"unsupported option", good[:len(good)-4] + statusPacket("option refname refs/heads/a\n") + "0000", "unknown", false},
		{"oversized", strings.Repeat("x", ControlBytes+1), "unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := &Request{operation: "push", reportStatus: true, sideband: tc.sideband, actions: []gitpolicy.RefAction{{Ref: "refs/heads/a"}, {Ref: "refs/heads/b"}}}
			o := request.ObserveStatus()
			for i := 0; i < len(tc.wire); i += 3 {
				p := []byte(tc.wire[i:min(i+3, len(tc.wire))])
				n, err := o.Write(p)
				if n != len(p) || err != nil {
					t.Fatal("observer changed forwarding")
				}
			}
			if got := o.Result(); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
			outcomes := o.RefOutcomes([]string{"refs/heads/b", "refs/heads/a"})
			switch tc.want {
			case "reported_success":
				require.Equal(t, []string{"ok", "ok"}, outcomes)
			case "reported_partial":
				require.Equal(t, []string{"ng", "ok"}, outcomes)
			case "reported_failure":
				require.Equal(t, []string{"ng", "ng"}, outcomes)
			default:
				require.Nil(t, outcomes)
			}
			require.Nil(t, o.RefOutcomes([]string{"refs/heads/foreign"}))
		})
	}
}
