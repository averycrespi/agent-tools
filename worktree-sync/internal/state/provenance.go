package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

type Provenance struct {
	Version           int             `json:"version"`
	ExplicitWorktrees map[string]bool `json:"explicit_worktrees"`
}

type provenanceKey struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Identity   string `json:"identity"`
}

func LoadProvenance(path string) (*Provenance, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller supplies the private provenance path
	if errors.Is(err, os.ErrNotExist) {
		return &Provenance{Version: 1, ExplicitWorktrees: make(map[string]bool)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading provenance: %w", err)
	}
	var provenance Provenance
	if err := json.Unmarshal(data, &provenance); err != nil {
		return nil, fmt.Errorf("decoding provenance: %w", err)
	}
	if provenance.Version != 1 || provenance.ExplicitWorktrees == nil {
		return nil, fmt.Errorf("unsupported provenance version %d", provenance.Version)
	}
	return &provenance, nil
}

func provenanceMapKey(repository, path, identity string) string {
	data, _ := json.Marshal(provenanceKey{Repository: repository, Path: path, Identity: identity})
	return string(data)
}

func (p *Provenance) RecordExplicit(repository, path, identity string) {
	if p.ExplicitWorktrees == nil {
		p.ExplicitWorktrees = make(map[string]bool)
	}
	p.ExplicitWorktrees[provenanceMapKey(repository, path, identity)] = true
}

func (p *Provenance) Explicit(repository, path, identity string) bool {
	return p.ExplicitWorktrees[provenanceMapKey(repository, path, identity)]
}

func (p *Provenance) Remove(repository, path, identity string) {
	delete(p.ExplicitWorktrees, provenanceMapKey(repository, path, identity))
}

func (p *Provenance) Save(path string) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0o600)
}
