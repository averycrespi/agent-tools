package accesstarget

import "testing"

func TestMCPScopeRelations(t *testing.T) {
	server := MCP{ServerID: "server"}
	tool := Tool("server", "tool")
	for _, test := range []struct {
		name             string
		left, right      MCP
		covers, overlaps bool
	}{
		{"server covers server", server, server, true, true},
		{"server covers tool", server, tool, true, true},
		{"tool does not cover server", tool, server, false, true},
		{"same tool", tool, Tool("server", "tool"), true, true},
		{"different tool", tool, Tool("server", "other"), false, false},
		{"different server", server, Tool("other", "tool"), false, false},
		{"empty exact is not server scope", Tool("server", ""), server, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.left.Covers(test.right); got != test.covers {
				t.Fatalf("Covers = %v, want %v", got, test.covers)
			}
			if got := test.left.Overlaps(test.right); got != test.overlaps {
				t.Fatalf("Overlaps = %v, want %v", got, test.overlaps)
			}
			if test.left.Overlaps(test.right) != test.right.Overlaps(test.left) {
				t.Fatal("overlap must be symmetric")
			}
		})
	}
}

func TestMCPToolRetainsExactBytesWithoutResolution(t *testing.T) {
	name := "nested.tool-1"
	target := Tool("00000000000000000000000000", name)
	name = "changed"
	if target.ToolName() != "nested.tool-1" || target.ToolName() == name || target.UpstreamName == nil {
		t.Fatal("tool construction lost exact coordinates")
	}
	if (MCP{}).ToolName() != "" {
		t.Fatal("server scope must not invent a tool name")
	}
}
