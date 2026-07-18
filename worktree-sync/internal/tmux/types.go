package tmux

const (
	SocketName     = "wts"
	MetadataSchema = 1
)

type Metadata struct {
	Schema     int    `json:"schema"`
	Repository string `json:"repository"`
	Role       string `json:"role"`
	Identity   string `json:"identity"`
}

type Window struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Metadata Metadata `json:"metadata"`
}

type Session struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Metadata Metadata `json:"metadata"`
	Windows  []Window `json:"windows"`
}

type Snapshot struct {
	Complete bool      `json:"complete"`
	Sessions []Session `json:"sessions"`
}

func ValidOwnedWindow(metadata Metadata, repository string) bool {
	if metadata.Schema != MetadataSchema || metadata.Repository != repository {
		return false
	}
	if metadata.Role == "base" {
		return metadata.Identity == repository
	}
	return metadata.Role == "worktree" && metadata.Identity != "" && metadata.Identity != repository
}
