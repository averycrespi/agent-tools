package paths

import (
	"crypto/sha256"
	"encoding/hex"
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

// ReadInitializationInput recognizes retained historical import evidence. It
// grants no import, resume, replay, replacement or cleanup capability.
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
