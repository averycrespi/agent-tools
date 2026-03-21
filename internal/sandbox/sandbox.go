package sandbox

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/sandbox-manager/internal/config"
	"github.com/averycrespi/agent-tools/sandbox-manager/internal/lima"
)

// LimaClient is the interface for Lima VM operations.
type LimaClient interface {
	Status() (lima.Status, error)
	Create(templatePath string) error
	Start() error
	Stop() error
	Delete() error
	Copy(localPath, guestPath string) error
	Exec(args ...string) ([]byte, error)
	Shell(args ...string) error
}

// Service manages the sandbox VM lifecycle.
type Service struct {
	lima   LimaClient
	config config.Config
	logger *slog.Logger
}

// NewService returns a new sandbox service.
func NewService(lima LimaClient, cfg config.Config, logger *slog.Logger) *Service {
	return &Service{lima: lima, config: cfg, logger: logger}
}

// Status returns the current VM status.
func (s *Service) Status() (lima.Status, error) {
	return s.lima.Status()
}

// Create creates, starts, and provisions the sandbox.
func (s *Service) Create() error {
	status, err := s.lima.Status()
	if err != nil {
		return err
	}

	switch status {
	case lima.StatusRunning:
		s.logger.Debug("VM already running, re-provisioning")
		return s.Provision()

	case lima.StatusStopped:
		s.logger.Debug("VM stopped, starting and provisioning")
		if err := s.lima.Start(); err != nil {
			return err
		}
		return s.Provision()

	case lima.StatusNotCreated:
		s.logger.Debug("creating VM")
		params, err := HostTemplateParams()
		if err != nil {
			return err
		}
		params.Image = s.config.Image
		params.CPUs = s.config.CPUs
		params.Memory = s.config.Memory
		params.Disk = s.config.Disk
		params.Mounts = s.config.Mounts

		rendered, err := RenderTemplate(params)
		if err != nil {
			return err
		}

		tmpFile, err := writeTempFile(rendered)
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(tmpFile) }()

		if err := s.lima.Create(tmpFile); err != nil {
			return err
		}
		return s.Provision()
	}

	return nil
}

// Start starts a stopped VM.
func (s *Service) Start() error {
	status, err := s.lima.Status()
	if err != nil {
		return err
	}

	switch status {
	case lima.StatusNotCreated:
		return fmt.Errorf("VM not created: run \"sb create\" first")
	case lima.StatusRunning:
		s.logger.Debug("VM already running")
		return nil
	default:
		return s.lima.Start()
	}
}

// Stop stops the VM.
func (s *Service) Stop() error {
	status, err := s.lima.Status()
	if err != nil {
		return err
	}

	if status != lima.StatusRunning {
		s.logger.Debug("VM not running, nothing to stop")
		return nil
	}

	return s.lima.Stop()
}

// Destroy stops and removes the VM.
func (s *Service) Destroy() error {
	status, err := s.lima.Status()
	if err != nil {
		return err
	}

	if status == lima.StatusNotCreated {
		s.logger.Debug("VM not created, nothing to destroy")
		return nil
	}

	if status == lima.StatusRunning {
		s.logger.Debug("stopping VM before destroy")
		if err := s.lima.Stop(); err != nil {
			return err
		}
	}

	return s.lima.Delete()
}

// Provision copies files and runs scripts in the VM.
func (s *Service) Provision() error {
	status, err := s.lima.Status()
	if err != nil {
		return err
	}
	if status != lima.StatusRunning {
		return fmt.Errorf("VM not running: cannot provision")
	}

	// Copy files.
	for _, entry := range s.config.CopyPaths {
		src, dst := config.ParseCopyPath(entry)
		s.logger.Debug("copying file", "src", src, "dst", dst)

		// Create parent directory in the VM.
		parentDir := filepath.Dir(dst)
		if _, err := s.lima.Exec("mkdir", "-p", parentDir); err != nil {
			return fmt.Errorf("failed to create directory %q in VM: %w", parentDir, err)
		}

		if err := s.lima.Copy(src, dst); err != nil {
			return fmt.Errorf("failed to copy %q to %q: %w", src, dst, err)
		}
	}

	// Run scripts.
	for _, script := range s.config.Scripts {
		s.logger.Debug("running script", "script", script)

		tmpDst := "/tmp/sb-provision-script"
		if err := s.lima.Copy(script, tmpDst); err != nil {
			return fmt.Errorf("failed to copy script %q to VM: %w", script, err)
		}

		if _, err := s.lima.Exec("chmod", "+x", tmpDst); err != nil {
			return fmt.Errorf("failed to chmod script: %w", err)
		}

		if _, err := s.lima.Exec(tmpDst); err != nil {
			return fmt.Errorf("failed to run script %q: %w", script, err)
		}

		if _, err := s.lima.Exec("rm", "-f", tmpDst); err != nil {
			s.logger.Warn("failed to clean up temp script", "error", err)
		}
	}

	return nil
}

// Shell opens a shell in the VM.
func (s *Service) Shell(args ...string) error {
	return s.lima.Shell(args...)
}

func writeTempFile(content string) (string, error) {
	f, err := os.CreateTemp("", "sb-lima-*.yaml")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}

	return f.Name(), nil
}
