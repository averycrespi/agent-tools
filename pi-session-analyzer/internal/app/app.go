// Package app orchestrates ingestion and independent detector runs.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/detect"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/store"
)

// IngestResult contains privacy-safe aggregate counts.
type IngestResult struct {
	Files, Changed, Unchanged, Records, Malformed, Unknown, SchemaDrift int
}

type Service struct {
	store     *store.Store
	detectors []detect.Detector
}

func New(database *store.Store, detectors []detect.Detector) *Service {
	return &Service{store: database, detectors: detectors}
}

// Ingest indexes all JSONL files without pruning absent sources.
func (s *Service) Ingest(ctx context.Context, sessionsDir string) (IngestResult, error) {
	var result IngestResult
	var runErrors []error
	err := filepath.WalkDir(sessionsDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			runErrors = append(runErrors, walkErr)
			return nil
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
			return nil
		}
		result.Files++
		info, err := entry.Info()
		if err != nil {
			runErrors = append(runErrors, fmt.Errorf("stat %s: %w", path, err))
			return nil
		}
		meta := store.SourceMeta{Path: path, Size: info.Size(), ModTimeNS: info.ModTime().UnixNano()}
		unchanged, err := s.store.SourceUnchanged(ctx, meta)
		if err != nil {
			runErrors = append(runErrors, err)
			return nil
		}
		if unchanged {
			result.Unchanged++
			return nil
		}
		file, err := os.Open(path) //nolint:gosec // path comes from the requested sessions directory walk.
		if err != nil {
			runErrors = append(runErrors, fmt.Errorf("open %s: %w", path, err))
			return nil
		}
		session, parseErr := ingest.Parse(file)
		closeErr := file.Close()
		if parseErr != nil {
			runErrors = append(runErrors, fmt.Errorf("parse %s: %w", path, parseErr))
			return nil
		}
		if closeErr != nil {
			runErrors = append(runErrors, fmt.Errorf("close %s: %w", path, closeErr))
			return nil
		}
		if err = s.store.ReplaceSession(ctx, session, meta); err != nil {
			runErrors = append(runErrors, fmt.Errorf("store %s: %w", path, err))
			return nil
		}
		result.Changed++
		result.Records += session.Stats.Total
		result.Malformed += session.Stats.Malformed
		result.Unknown += session.Stats.Unknown
		result.SchemaDrift += session.Stats.SchemaDrift
		if err = s.detectSession(ctx, session); err != nil {
			runErrors = append(runErrors, err)
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, errors.Join(runErrors...)
}

// Detect recomputes all detectors for all sessions or one exact/unique prefix.
func (s *Service) Detect(ctx context.Context, sessionPrefix string) error {
	var ids []string
	if sessionPrefix != "" {
		id, err := s.store.ResolveSession(ctx, sessionPrefix)
		if err != nil {
			return err
		}
		ids = []string{id}
	} else {
		var err error
		ids, err = s.store.SessionIDs(ctx)
		if err != nil {
			return err
		}
	}
	var runErrors []error
	for _, id := range ids {
		session, err := s.store.LoadSession(ctx, id)
		if err != nil {
			runErrors = append(runErrors, err)
			continue
		}
		if err = s.detectSession(ctx, session); err != nil {
			runErrors = append(runErrors, err)
		}
	}
	return errors.Join(runErrors...)
}

func (s *Service) detectSession(ctx context.Context, session ingest.Session) error {
	var runErrors []error
	for _, detector := range s.detectors {
		findings, err := detector.Run(session)
		if err != nil {
			if saveErr := s.store.SaveDetectorFailure(ctx, session.ID, detector.Name, err); saveErr != nil {
				runErrors = append(runErrors, saveErr)
			}
			runErrors = append(runErrors, fmt.Errorf("detector %s for %s: %w", detector.Name, session.ID, err))
			continue
		}
		if err = s.store.SaveDetectorSuccess(ctx, session.ID, detector.Name, findings); err != nil {
			runErrors = append(runErrors, fmt.Errorf("save detector %s for %s: %w", detector.Name, session.ID, err))
		}
	}
	return errors.Join(runErrors...)
}
