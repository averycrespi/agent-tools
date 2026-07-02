package rules

import "github.com/averycrespi/agent-tools/mcp-broker/internal/config"

func cloneRules(rs []config.RuleConfig) []config.RuleConfig {
	if rs == nil {
		return nil
	}
	out := make([]config.RuleConfig, len(rs))
	for i, r := range rs {
		out[i] = cloneRule(r)
	}
	return out
}

func cloneRule(r config.RuleConfig) config.RuleConfig {
	out := r
	if r.Args != nil {
		out.Args = make([]config.ArgPattern, len(r.Args))
		for i, arg := range r.Args {
			out.Args[i] = arg
			if arg.Match != nil {
				out.Args[i].Match = append([]byte(nil), arg.Match...)
			}
		}
	}
	return out
}
