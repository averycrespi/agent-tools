package git

import (
	"bytes"
	"fmt"
	"strings"
)

type Worktree struct {
	Path      string `json:"path"`
	HEAD      string `json:"head"`
	Branch    string `json:"branch,omitempty"`
	Detached  bool   `json:"detached,omitempty"`
	Locked    string `json:"locked,omitempty"`
	Prunable  string `json:"prunable,omitempty"`
	Identity  string `json:"identity,omitempty"`
	Eligible  bool   `json:"eligible"`
	Exclusion string `json:"exclusion,omitempty"`
}

func ParsePorcelainZ(data []byte) ([]Worktree, error) {
	if len(data) == 0 {
		return []Worktree{}, nil
	}
	if data[len(data)-1] != 0 {
		return nil, fmt.Errorf("porcelain output is not NUL terminated")
	}
	fields := bytes.Split(data, []byte{0})
	result := make([]Worktree, 0)
	var current *Worktree
	for i, raw := range fields[:len(fields)-1] {
		if len(raw) == 0 {
			if current != nil {
				result = append(result, *current)
				current = nil
			}
			continue
		}
		key, value, _ := strings.Cut(string(raw), " ")
		if key == "worktree" {
			if current != nil {
				return nil, fmt.Errorf("record %d missing separator", i)
			}
			if value == "" {
				return nil, fmt.Errorf("worktree path is empty")
			}
			current = &Worktree{Path: value}
			continue
		}
		if current == nil {
			return nil, fmt.Errorf("record field %q appears before worktree", key)
		}
		switch key {
		case "HEAD":
			current.HEAD = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "detached":
			current.Detached = true
		case "locked":
			current.Locked = value
		case "prunable":
			current.Prunable = value
		case "bare":
			return nil, fmt.Errorf("bare worktree record is unsupported")
		default:
			return nil, fmt.Errorf("unknown porcelain field %q", key)
		}
	}
	if current != nil {
		result = append(result, *current)
	}
	for i, worktree := range result {
		if worktree.Path == "" || worktree.HEAD == "" {
			return nil, fmt.Errorf("worktree record %d is incomplete", i)
		}
	}
	return result, nil
}
