package invocation

import "github.com/averycrespi/agent-tools/mcp-gateway/internal/accesstarget"

// MCPDetails contains only MCP call evidence, including partial malformed call
// capture. An absent route is unresolved; it is not a server-wide access target.
type MCPDetails struct {
	RequestedName     *string
	RedactedArguments []byte
	Route             *RouteEvidence
}

// RouteEvidence pins an exact MCP target and its descriptor. Synthetic local
// and downstream targets share this shape and remain distinctions within MCP.
type RouteEvidence struct {
	Target                accesstarget.MCP
	ToolID                string
	DescriptorRevision    string
	DescriptorFingerprint string
}

func cloneMCPDetails(value MCPDetails) MCPDetails {
	clone := value
	clone.RedactedArguments = append([]byte(nil), value.RedactedArguments...)
	if value.RequestedName != nil {
		name := *value.RequestedName
		clone.RequestedName = &name
	}
	if value.Route != nil {
		route := *value.Route
		if route.Target.UpstreamName != nil {
			name := *route.Target.UpstreamName
			route.Target.UpstreamName = &name
		}
		clone.Route = &route
	}
	return clone
}
