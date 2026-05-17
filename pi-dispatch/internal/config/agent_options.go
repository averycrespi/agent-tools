package config

type AgentOptions struct {
	Provider                  string   `json:"provider,omitempty"`
	Model                     string   `json:"model,omitempty"`
	Thinking                  string   `json:"thinking,omitempty"`
	Tools                     []string `json:"tools,omitempty"`
	DisableBuiltinTools       bool     `json:"disable_builtin_tools,omitempty"`
	DisableAllTools           bool     `json:"disable_all_tools,omitempty"`
	Extensions                []string `json:"extensions,omitempty"`
	DisableExtensionDiscovery bool     `json:"disable_extension_discovery,omitempty"`
	Skills                    []string `json:"skills,omitempty"`
	DisableSkillDiscovery     bool     `json:"disable_skill_discovery,omitempty"`
	DisableContextFiles       bool     `json:"disable_context_files,omitempty"`
	SystemPrompt              string   `json:"system_prompt,omitempty"`
	AppendSystemPrompt        string   `json:"append_system_prompt,omitempty"`
	SessionDir                string   `json:"session_dir,omitempty"`
}

func RenderPiArgv(agent AgentOptions) []string {
	argv := []string{"pi", "--mode", "rpc"}
	addValue := func(flag, value string) {
		if value != "" {
			argv = append(argv, flag, value)
		}
	}
	addList := func(flag string, values []string) {
		for _, value := range values {
			if value != "" {
				argv = append(argv, flag, value)
			}
		}
	}
	addBool := func(flag string, value bool) {
		if value {
			argv = append(argv, flag)
		}
	}
	addValue("--provider", agent.Provider)
	addValue("--model", agent.Model)
	addValue("--thinking", agent.Thinking)
	addList("--tools", agent.Tools)
	addBool("--no-builtin-tools", agent.DisableBuiltinTools)
	addBool("--no-tools", agent.DisableAllTools)
	addList("--extension", agent.Extensions)
	addBool("--no-extensions", agent.DisableExtensionDiscovery)
	addList("--skill", agent.Skills)
	addBool("--no-skills", agent.DisableSkillDiscovery)
	addBool("--no-context-files", agent.DisableContextFiles)
	addValue("--system-prompt", agent.SystemPrompt)
	addValue("--append-system-prompt", agent.AppendSystemPrompt)
	addValue("--session-dir", agent.SessionDir)
	return argv
}

func ApplyAgentOverrides(base, overrides AgentOptions) AgentOptions {
	if overrides.Provider != "" {
		base.Provider = overrides.Provider
	}
	if overrides.Model != "" {
		base.Model = overrides.Model
	}
	if overrides.Thinking != "" {
		base.Thinking = overrides.Thinking
	}
	if overrides.Tools != nil {
		base.Tools = overrides.Tools
	}
	if overrides.DisableBuiltinTools {
		base.DisableBuiltinTools = true
	}
	if overrides.DisableAllTools {
		base.DisableAllTools = true
	}
	if overrides.Extensions != nil {
		base.Extensions = overrides.Extensions
	}
	if overrides.DisableExtensionDiscovery {
		base.DisableExtensionDiscovery = true
	}
	if overrides.Skills != nil {
		base.Skills = overrides.Skills
	}
	if overrides.DisableSkillDiscovery {
		base.DisableSkillDiscovery = true
	}
	if overrides.DisableContextFiles {
		base.DisableContextFiles = true
	}
	if overrides.SystemPrompt != "" {
		base.SystemPrompt = overrides.SystemPrompt
	}
	if overrides.AppendSystemPrompt != "" {
		base.AppendSystemPrompt = overrides.AppendSystemPrompt
	}
	if overrides.SessionDir != "" {
		base.SessionDir = overrides.SessionDir
	}
	return base
}
