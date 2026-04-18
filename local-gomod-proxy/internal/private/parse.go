package private

import (
	"fmt"
	"strings"

	"golang.org/x/mod/module"
)

// Artifact identifies which file the client is requesting.
type Artifact int

const (
	_ Artifact = iota
	ArtifactInfo
	ArtifactMod
	ArtifactZip
	ArtifactList
	ArtifactLatest
)

// Request is a parsed Go module proxy request.
type Request struct {
	Module   string
	Version  string // empty for List and Latest
	Artifact Artifact
}

// ParseRequest parses a proxy URL path (with leading slash) into a Request.
// Rejects malformed paths, unsupported artifacts, and path traversal.
func ParseRequest(path string) (Request, error) {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" || strings.Contains(trimmed, "..") {
		return Request{}, fmt.Errorf("invalid path: %q", path)
	}

	// /@latest form
	if idx := strings.LastIndex(trimmed, "/@latest"); idx >= 0 && idx == len(trimmed)-len("/@latest") {
		modEsc := trimmed[:idx]
		mod, err := module.UnescapePath(modEsc)
		if err != nil {
			return Request{}, fmt.Errorf("invalid module path: %w", err)
		}
		return Request{Module: mod, Artifact: ArtifactLatest}, nil
	}

	// /@v/... form
	idx := strings.Index(trimmed, "/@v/")
	if idx < 0 {
		return Request{}, fmt.Errorf("path missing /@v/ or /@latest: %q", path)
	}
	modEsc := trimmed[:idx]
	rest := trimmed[idx+len("/@v/"):]

	mod, err := module.UnescapePath(modEsc)
	if err != nil {
		return Request{}, fmt.Errorf("invalid module path: %w", err)
	}

	if rest == "list" {
		return Request{Module: mod, Artifact: ArtifactList}, nil
	}

	// <version>.<ext>
	dot := strings.LastIndex(rest, ".")
	if dot < 0 {
		return Request{}, fmt.Errorf("invalid artifact: %q", rest)
	}
	verEsc, ext := rest[:dot], rest[dot+1:]
	ver, err := module.UnescapeVersion(verEsc)
	if err != nil {
		return Request{}, fmt.Errorf("invalid version: %w", err)
	}

	var art Artifact
	switch ext {
	case "info":
		art = ArtifactInfo
	case "mod":
		art = ArtifactMod
	case "zip":
		art = ArtifactZip
	default:
		return Request{}, fmt.Errorf("unsupported artifact extension: %q", ext)
	}
	return Request{Module: mod, Version: ver, Artifact: art}, nil
}
