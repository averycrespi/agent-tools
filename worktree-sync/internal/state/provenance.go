package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

type Provenance struct {
	Version       int             `json:"version"`
	ExplicitPaths map[string]bool `json:"explicit_paths"`
}

func LoadProvenance(path string) (*Provenance, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller supplies the private provenance path
	if errors.Is(err, os.ErrNotExist) {
		return &Provenance{Version: 1, ExplicitPaths: make(map[string]bool)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading provenance: %w", err)
	}
	var provenance Provenance
	if err := json.Unmarshal(data, &provenance); err != nil {
		return nil, fmt.Errorf("decoding provenance: %w", err)
	}
	if provenance.Version != 1 || provenance.ExplicitPaths == nil {
		return nil, fmt.Errorf("unsupported provenance version %d", provenance.Version)
	}
	return &provenance, nil
}

func (p *Provenance) RecordExplicit(path string) {
	if p.ExplicitPaths == nil {
		p.ExplicitPaths = make(map[string]bool)
	}
	p.ExplicitPaths[path] = true
}
func (p *Provenance) Explicit(path string) bool { return p.ExplicitPaths[path] }
func (p *Provenance) Remove(path string)        { delete(p.ExplicitPaths, path) }
func (p *Provenance) Save(path string) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0o600)
}
