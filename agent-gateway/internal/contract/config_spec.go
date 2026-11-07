package contract

// ConfigSpecMaxBytes preserves individual server/grant bounds inside the larger
// bounded complete-document representation.
func ConfigSpecMaxBytes(kind string) int64 {
	switch kind {
	case "mcp_server", "mcp_grant", "http_grant", "git_grant":
		return 2 * 1024 * 1024
	default:
		return ConfigExportMaxBytes
	}
}

// ConfigSpecDestination selects the shared closed wire schema. It allocates only
// a DTO; domain validation, dependency resolution and creation remain owner-local.
func ConfigSpecDestination(kind string) any {
	switch kind {
	case "agent":
		return &ConfigAgentSpec{}
	case "mcp_server":
		return &ConfigServerSpec{}
	case "mcp_grant":
		return &ConfigMCPGrantSpec{}
	case "http_credential":
		return &ConfigHTTPCredentialSpec{}
	case "http_grant":
		return &ConfigHTTPGrantSpec{}
	case "git_credential":
		return &ConfigGitCredentialSpec{}
	case "git_repository":
		return &ConfigGitRepositorySpec{}
	case "git_grant":
		return &ConfigGitGrantSpec{}
	case "git_routing":
		return &ConfigGitRoutingSpec{}
	default:
		return nil
	}
}
