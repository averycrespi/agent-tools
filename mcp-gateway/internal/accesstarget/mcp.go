// Package accesstarget owns the internal access target vocabulary, not identity,
// namespace resolution, policy authority, or public and durable representations.
package accesstarget

// MCP identifies an immutable server and either its whole tool scope (nil
// UpstreamName) or one exact upstream tool. MCP is the only supported domain.
// A value is not evidence of validation, existence, descriptor eligibility, or
// admission. Those checks belong to the consuming boundary; in particular,
// synthetic targets are valid for grants/calls but not for grant requests.
// Consumers must not mutate an upstream name through a shared pointer.
type MCP struct {
	ServerID     string
	UpstreamName *string
}

// Tool constructs exact scope without resolving or validating its coordinates.
func Tool(serverID, upstreamName string) MCP {
	return MCP{ServerID: serverID, UpstreamName: &upstreamName}
}

// ToolName returns the exact name, or empty for server scope. Exact-call
// boundaries still validate this name; empty exact names are not valid calls.
func (target MCP) ToolName() string {
	if target.UpstreamName == nil {
		return ""
	}
	return *target.UpstreamName
}

// Covers compares target scope only, without granting authority or considering
// constraints, expiry, DENY precedence, or published descriptor facts.
func (target MCP) Covers(other MCP) bool {
	return target.ServerID == other.ServerID && (target.UpstreamName == nil ||
		other.UpstreamName != nil && *target.UpstreamName == *other.UpstreamName)
}

// Overlaps is the conservative target-scope intersection used by DENY checks.
// It deliberately does not attempt constraint or descriptor analysis.
func (target MCP) Overlaps(other MCP) bool {
	return target.Covers(other) || other.Covers(target)
}
