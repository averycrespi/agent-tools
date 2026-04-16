package grants

import "errors"

type toolGroup struct {
	tool  string
	flags []string
}

// splitByTool separates command-line args into global flags and tool-scoped
// flag groups. The first --tool delimits the boundary between globals and
// groups; every subsequent --tool opens a new group.
func splitByTool(args []string) ([]string, []toolGroup, error) {
	var (
		global []string
		groups []toolGroup
		cur    *toolGroup
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--tool" {
			if i+1 >= len(args) {
				return nil, nil, errors.New("--tool requires a name")
			}
			groups = append(groups, toolGroup{tool: args[i+1]})
			cur = &groups[len(groups)-1]
			i++
			continue
		}
		if cur == nil {
			global = append(global, a)
		} else {
			cur.flags = append(cur.flags, a)
		}
	}
	if len(groups) == 0 {
		return nil, nil, errors.New("at least one --tool is required")
	}
	return global, groups, nil
}
