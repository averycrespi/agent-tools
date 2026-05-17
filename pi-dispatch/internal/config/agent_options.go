package config

type AgentOptions struct {
	Provider                  string
	Model                     string
	Thinking                  string
	Tools                     []string
	DisableBuiltinTools       bool
	DisableAllTools           bool
	Extensions                []string
	DisableExtensionDiscovery bool
	Skills                    []string
	DisableSkillDiscovery     bool
	DisableContextFiles       bool
	SystemPrompt              string
	AppendSystemPrompt        string
	SessionDir                string
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
