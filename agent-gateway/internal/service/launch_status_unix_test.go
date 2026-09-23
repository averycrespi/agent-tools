//go:build darwin || linux

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Synthetic launchctl-shaped blocks: regression evidence, not native qualification.
func TestServiceStatusNestedLaunchFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(string) string
		want   string
	}{
		{"coalitions", func(s string) string {
			return strings.Replace(s, "\targuments = {", "\tresource coalition = {\n\t\tid = 123\n\t\tstate = active\n\t}\n\tjetsam coalition = {\n\t\tstate = active\n\t}\n\targuments = {", 1)
		}, "running"},
		{"nested identity", func(s string) string {
			return strings.Replace(s, "\targuments = {", "\tnested = {\n\t\tpath = /wrong\n\t\tprogram = /wrong\n\t\tpid = 999\n\t\tstate = active\n\t\targuments = {\n\t\t\t/wrong\n\t\t}\n\t}\n\targuments = {", 1)
		}, "running"},
		{"spaces", func(s string) string { return strings.ReplaceAll(s, "\t", "    ") }, "running"},
		{"loaded exited", func(s string) string {
			return strings.Replace(strings.Replace(s, "\tpid = 123456\n", "", 1), "state = running", "state = not running", 1)
		}, "loaded-exited"},
		{"truncated arguments", func(s string) string { return strings.Replace(s, "\t}\n", "", 1) }, "unknown"},
		{"second service", func(s string) string { return s + s }, "unknown"},
		{"truncated", func(s string) string { return strings.TrimSuffix(s, "}\n") }, "unknown"},
		{"extra close", func(s string) string { return s + "}\n" }, "unknown"},
		{"wrong target", func(s string) string { return "wrong" + s }, "unknown"},
		{"duplicate state", func(s string) string {
			return strings.Replace(s, "\tstate = running", "\tstate = running\n\tstate = running", 1)
		}, "unknown"},
		{"duplicate pid", func(s string) string {
			return strings.Replace(s, "\tpid = 123456", "\tpid = 123456\n\tpid = 123456", 1)
		}, "unknown"},
		{"nested only state", func(s string) string {
			return strings.Replace(s, "\tstate = running", "\tnested = {\n\t\tstate = running\n\t}", 1)
		}, "unknown"},
		{"nested only pid", func(s string) string {
			return strings.Replace(s, "\tpid = 123456", "\tnested = {\n\t\tpid = 123456\n\t}", 1)
		}, "unknown"},
		{"unclosed nested", func(s string) string {
			return strings.Replace(s, "\tstate = running", "\tnested = {\n\tstate = running", 1)
		}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.install(t)
			f.loaded, f.running = true, true
			data, code, err := f.run(t.Context(), "/bin/launchctl", "print", f.m.target())
			require.NoError(t, err)
			require.Zero(t, code)
			f.m.run = func(_ context.Context, name string, args ...string) ([]byte, int, error) {
				require.Equal(t, "/bin/launchctl", name)
				require.Equal(t, []string{"print", f.m.target()}, args)
				return []byte(tc.change(string(data))), 0, nil
			}
			result, err := f.m.execute(t.Context(), "status", Changes{})
			require.NoError(t, err)
			require.True(t, result.Installed)
			require.Equal(t, tc.want, result.Launchd)
			require.Equal(t, "not-ready", result.Readiness)
			require.Empty(t, f.mutations)
		})
	}
}

func TestServiceLaunchIdentityRefusesImpersonation(t *testing.T) {
	for _, field := range []string{"path", "program", "state", "pid", "arguments"} {
		for _, mode := range []string{"nested only", "duplicate", "mismatch", "missing"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				f := newFixture(t)
				f.install(t)
				f.loaded, f.running = true, true
				data, _, err := f.run(t.Context(), "/bin/launchctl", "print", f.m.target())
				require.NoError(t, err)
				text := string(data)
				start := strings.Index(text, "\t"+field+" = ")
				require.NotEqual(t, -1, start)
				end := start + strings.Index(text[start:], "\n") + 1
				if field == "arguments" {
					end = start + strings.Index(text[start:], "\t}\n") + len("\t}\n")
				}
				original := text[start:end]
				replacement := ""
				switch mode {
				case "nested only":
					replacement = "\tnested = {\n\t" + strings.ReplaceAll(strings.TrimSuffix(original, "\n"), "\n", "\n\t") + "\n\t}\n"
				case "duplicate":
					replacement = original + original
				case "mismatch":
					replacement = "\t" + field + " = wrong\n"
					if field == "state" {
						// A running PID is authoritative under the existing state policy.
						return
					}
				}
				text = text[:start] + replacement + text[end:]
				f.m.run = func(_ context.Context, name string, args ...string) ([]byte, int, error) {
					require.Equal(t, "/bin/launchctl", name)
					require.Equal(t, []string{"print", f.m.target()}, args)
					return []byte(text), 0, nil
				}
				result, err := f.m.execute(t.Context(), "status", Changes{})
				require.NoError(t, err)
				require.Equal(t, "unknown", result.Launchd)
				_, err = f.m.execute(t.Context(), "restart", Changes{})
				require.Error(t, err)
				require.Empty(t, f.mutations)
			})
		}
	}
}
