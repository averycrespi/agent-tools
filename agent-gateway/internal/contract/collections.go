package contract

// CollectionContract describes the ordinary operator query independently of
// filter presence. Projection selectors are explicit and never table modes.
type CollectionContract struct {
	Pattern       string
	DefaultOrder  string
	DefaultLimit  int
	MaximumLimit  int
	SuccessSchema string
	QueryMembers  []string
	Projections   []string
}

var collectionContracts = []CollectionContract{
	{Pattern: "/api/v2/admin-credentials", DefaultOrder: "id ascending", DefaultLimit: 50, MaximumLimit: 100, SuccessSchema: "Page<AdminCredential>", QueryMembers: []string{"cursor", "limit"}},
	{Pattern: "/api/v2/backups", DefaultOrder: "id ascending", DefaultLimit: 50, MaximumLimit: 100, SuccessSchema: "Page<Backup>", QueryMembers: []string{"cursor", "limit"}},
	{Pattern: "/api/v2/mcp/servers", DefaultOrder: "name ascending", DefaultLimit: 50, MaximumLimit: 50, SuccessSchema: "Page<Server>", QueryMembers: []string{"cursor", "limit", "name", "namespace", "status", "sort", "direction"}},
	{Pattern: "/api/v2/mcp/catalog", DefaultOrder: "tool ascending", DefaultLimit: CollectionPageDefault, MaximumLimit: CatalogPageMaximum, SuccessSchema: "CatalogPage", QueryMembers: []string{"cursor", "limit", "tool", "server", "status", "sort", "direction"}},
	{Pattern: "/api/v2/mcp/servers/{id}/descriptors", DefaultOrder: "last-seen descending", DefaultLimit: CollectionPageDefault, MaximumLimit: CatalogPageMaximum, SuccessSchema: "Page<ToolDescriptor>", QueryMembers: []string{"cursor", "limit", "tool", "status", "sort", "direction", "projection"}, Projections: []string{"full", "summary"}},
	{Pattern: "/api/v2/mcp/servers/{id}/operations", DefaultOrder: "created descending", DefaultLimit: CollectionPageDefault, MaximumLimit: OperationPageMaximum, SuccessSchema: "QueryPage<ServerOperation>", QueryMembers: []string{"cursor", "limit", "action", "status", "sort", "direction", "projection"}, Projections: []string{"active"}},
	{Pattern: "/api/v2/mcp/servers/{id}/oauth-flows", DefaultOrder: "insertion_sequence ascending", DefaultLimit: 50, MaximumLimit: 100, SuccessSchema: "Page<ServerAuthFlow>", QueryMembers: []string{"cursor", "limit"}},
	{Pattern: "/api/v2/principals", DefaultOrder: "name ascending", DefaultLimit: 50, MaximumLimit: 100, SuccessSchema: "QueryPage<Principal>", QueryMembers: []string{"cursor", "limit", "name", "state", "visibility", "sort", "direction"}},
	{Pattern: "/api/v2/mcp/grants", DefaultOrder: "description ascending", DefaultLimit: 50, MaximumLimit: 100, SuccessSchema: "QueryPage<GrantTableItem>", QueryMembers: []string{"cursor", "limit", "principal_id", "server_id", "identity", "principal", "target", "effect", "state", "sort", "direction"}},
	{Pattern: "/api/v2/mcp/grant-requests", DefaultOrder: "submitted descending", DefaultLimit: 50, MaximumLimit: 100, SuccessSchema: "QueryPage<GrantRequestTableItem>", QueryMembers: []string{"cursor", "limit", "principal_id", "state", "request", "principal", "target", "scope", "sort", "direction"}},
	{Pattern: "/api/v2/mcp/invocations", DefaultOrder: "insertion_sequence descending", DefaultLimit: 50, MaximumLimit: 100, SuccessSchema: "InvocationPage", QueryMembers: []string{"cursor", "limit", "principal_id", "server_id", "requested_name", "admission_class", "decision", "outcome", "tool", "principal", "search_locale"}},
	{Pattern: "/api/v2/audit-events", DefaultOrder: "insertion_sequence descending", DefaultLimit: 50, MaximumLimit: 100, SuccessSchema: "AuditPage", QueryMembers: []string{"cursor", "limit", "generation", "actor_type", "credential_id", "category", "action", "target_type", "target_id", "outcome", "correlation_id", "from", "until"}},
}

func ServerStatusFilters() []string {
	return []string{"ready", "connecting", "authorization_required", "authentication_unavailable", "capacity_saturated", "disabled", "deleted", "needs_attention"}
}

func CollectionContracts() []CollectionContract {
	result := append([]CollectionContract(nil), collectionContracts...)
	for index := range result {
		result[index].QueryMembers = append([]string(nil), result[index].QueryMembers...)
		result[index].Projections = append([]string(nil), result[index].Projections...)
	}
	return result
}
