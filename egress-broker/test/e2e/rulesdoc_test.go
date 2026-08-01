//go:build e2e

package e2e_test

import (
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"
)

// rule mirrors the rules.json schema, so tests write policy the same way an
// operator would rather than through an internal type.
type rule struct {
	Name   string         `json:"name"`
	Host   string         `json:"host"`
	Path   string         `json:"path,omitempty"`
	Method string         `json:"method,omitempty"`
	Ports  []int          `json:"ports,omitempty"`
	Mode   string         `json:"mode"`
	Inject map[string]any `json:"inject,omitempty"`
}

// rulesDoc builds a rules document.
func rulesDoc(fallthrough_ string, rs ...rule) map[string]any {
	list := make([]any, 0, len(rs))
	for _, r := range rs {
		list = append(list, r)
	}
	return map[string]any{"fallthrough": fallthrough_, "rules": list}
}

// inject builds an inject block.
func inject(set map[string]string, remove ...string) map[string]any {
	out := map[string]any{"set": set}
	if len(remove) > 0 {
		out["remove"] = remove
	}
	return out
}

func hostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func dialProxy(t *testing.T, s *stack) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", s.proxyAddr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dialling the proxy: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	return conn
}

func writeRaw(t *testing.T, conn net.Conn, s string) {
	t.Helper()
	if _, err := conn.Write([]byte(s)); err != nil {
		t.Fatalf("writing to the proxy: %v", err)
	}
}

var _ = fmt.Sprintf
