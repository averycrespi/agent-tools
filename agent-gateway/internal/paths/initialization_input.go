package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func validInitializationDigest(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == sha256.Size && hex.EncodeToString(data) == value
}

// RetainInitializationInput publishes the already validated bytes exclusively.
// Partial/uncertain publication is retained, never replaced or sourced again.
func RetainInitializationInput(owner *Ownership, plan InitializationPlan, data []byte) error {
	return retainInitializationInput(owner, plan, data, initializationPublication{})
}

func retainInitializationInput(owner *Ownership, plan InitializationPlan, data []byte, publication initializationPublication) error {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return err
	}
	if !validInitializationDigest(plan.ImportDigest) || fmt.Sprintf("%x", sha256.Sum256(data)) != plan.ImportDigest {
		return ErrUnsafePath
	}
	return writeInitializationFile(filepath.Join(layout.Root, InitializationName), "input", data, publication)
}

func ReadInitializationInput(layout Layout, plan InitializationPlan) ([]byte, error) {
	path := filepath.Join(layout.Root, InitializationName, "input")
	if err := ValidateOwnerOnlyFile(path); err != nil {
		return nil, err
	}
	file, err := openInitializationFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if err := validateOwnerOnlyFile(info, path); err != nil {
		return nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) || info.Size() > contract.ConfigExportMaxBytes {
		return nil, ErrUnsafePath
	}
	data, err := io.ReadAll(io.LimitReader(file, contract.ConfigExportMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > contract.ConfigExportMaxBytes || fmt.Sprintf("%x", sha256.Sum256(data)) != plan.ImportDigest {
		return nil, ErrUnsafePath
	}
	return data, nil
}

// RemoveInitializationInput is permitted only after the complete evidence seal.
// The plan and transaction receipt remain; a missing input is then intentional.
func RemoveInitializationInput(owner *Ownership) error {
	return removeInitializationInput(owner, initializationPublication{})
}

func removeInitializationInput(owner *Ownership, publication initializationPublication) error {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return err
	}
	plan, complete, err := ReadInitialization(layout)
	if err != nil {
		return err
	}
	if !complete {
		return ErrInitializationIncomplete
	}
	if plan.ImportDigest == "" {
		return nil
	}
	removed, err := InitializationReceipt(layout, "input-removed")
	if err != nil {
		return err
	}
	if removed != "" {
		return nil
	}
	path := filepath.Join(layout.Root, InitializationName, "input")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if err := publication.directorySync(filepath.Dir(path)); err != nil {
			return err
		}
		return sealInitialization(owner, "input-removed", plan.ImportDigest, publication)
	} else if err != nil {
		return err
	}
	if _, err := ReadInitializationInput(layout, plan); err != nil {
		return err
	}
	if err := publication.after("input:before-remove"); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := publication.after("input:remove"); err != nil {
		return err
	}
	if err := publication.directorySync(filepath.Dir(path)); err != nil {
		return err
	}
	if err := publication.after("input:remove-directory-sync"); err != nil {
		return err
	}
	return sealInitialization(owner, "input-removed", plan.ImportDigest, publication)
}
