package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

const InitializationName = "initialization"

var ErrInitializationIncomplete = errors.New("initialization is incomplete; preserve artifacts and run init --resume or doctor")

// InitializationPlan is immutable consent and identity evidence.
// It is local to this exact selected root, not portable configuration input.
type initializationDocument struct {
	Plan        InitializationPlan `json:"plan"`
	DirectoryID string             `json:"directory_id"`
}

type InitializationPlan struct {
	Version      int    `json:"version"`
	Root         string `json:"root"`
	Bearer       string `json:"bearer"`
	Installation string `json:"installation"`
	Traffic      string `json:"traffic"`
	ImportDigest string `json:"import_digest,omitempty"`
}

// AcquireInitialization takes stopped ownership without a run marker. Callers
// must check the contents again under this lock before writing initial evidence.
func AcquireInitialization(root string) (*Ownership, error) {
	layout, err := Prepare(root)
	if err != nil {
		return nil, err
	}
	lock, err := acquireFileLock(layout.Lock)
	if err != nil {
		return nil, err
	}
	return &Ownership{layout: layout, lock: lock}, nil
}

func ReadInitialization(layout Layout) (InitializationPlan, bool, error) {
	var plan InitializationPlan
	if err := InspectRoot(layout.Root); err != nil {
		return plan, false, err
	}
	directory := filepath.Join(layout.Root, InitializationName)
	if err := InspectRoot(directory); err != nil {
		return plan, false, err
	}
	data, err := readInitializationFile(filepath.Join(directory, "plan"))
	if err != nil {
		return plan, false, err
	}
	var document initializationDocument
	if err := strictjson.Decode(data, &document, strictjson.Options{MaxBytes: 16384, MaxDepth: 4, RejectUnknownMembers: true}); err != nil {
		return plan, false, ErrUnsafePath
	}
	directoryID, err := initializationRootIdentity(layout.Root)
	if err != nil || document.DirectoryID != directoryID {
		return plan, false, ErrUnsafePath
	}
	plan = document.Plan
	canonicalPlanRoot, planRootErr := filepath.EvalSymlinks(plan.Root)
	canonicalRoot, rootErr := filepath.EvalSymlinks(layout.Root)
	if planRootErr != nil || rootErr != nil || canonicalPlanRoot != canonicalRoot || plan.Version != 1 || !filepath.IsAbs(plan.Root) || filepath.Clean(plan.Root) != plan.Root || !filepath.IsAbs(plan.Bearer) || !contract.ValidAuditID(plan.Installation) || !contract.ValidAuditID(plan.Traffic) || len(plan.Bearer) > 4096 || filepath.Clean(plan.Bearer) != plan.Bearer {
		return plan, false, ErrUnsafePath
	}
	handle, err := os.Open(directory)
	if err != nil {
		return plan, false, err
	}
	entries, readErr := handle.ReadDir(12)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, handle.Close()); err != nil {
		return plan, false, err
	}
	if len(entries) > 11 {
		return plan, false, ErrUnsafePath
	}
	if plan.ImportDigest != "" && !validInitializationDigest(plan.ImportDigest) {
		return plan, false, ErrUnsafePath
	}
	for _, entry := range entries {
		if entry.Name() == "input" && plan.ImportDigest != "" {
			if _, err := ReadInitializationInput(layout, plan); err != nil {
				return plan, false, err
			}
			continue
		}
		if entry.Name() == "import" && plan.ImportDigest == "" {
			return plan, false, ErrUnsafePath
		}
		if entry.Name() != "plan" && !initializationStep(entry.Name()) {
			return plan, false, ErrUnsafePath
		}
		data, err := readInitializationFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return plan, false, err
		}
		if entry.Name() == "custody-attempt" && string(data) != "attempt" {
			return plan, false, ErrUnsafePath
		}
	}
	attempt, err := InitializationReceipt(layout, "custody-attempt")
	if err != nil {
		return plan, false, err
	}
	control, err := InitializationReceipt(layout, "control")
	if err != nil {
		return plan, false, err
	}
	custody, err := InitializationReceipt(layout, "custody")
	if err != nil {
		return plan, false, err
	}
	if attempt != "" && control == "" || custody != "" && attempt == "" {
		return plan, false, ErrUnsafePath
	}
	missing := false
	steps := []string{"control", "custody", "traffic", "administrator", "ca", "certificate"}
	if plan.ImportDigest != "" {
		steps = append(steps, "import")
	}
	steps = append(steps, "complete")
	for _, name := range steps {
		receipt, err := InitializationReceipt(layout, name)
		if err != nil {
			return plan, false, err
		}
		if receipt == "" {
			missing = true
			continue
		}
		if missing {
			return plan, false, ErrUnsafePath
		}
		switch name {
		case "control":
			if receipt != plan.Installation {
				return plan, false, ErrUnsafePath
			}
		case "traffic":
			if receipt != plan.Traffic {
				return plan, false, ErrUnsafePath
			}
		case "import":
			if receipt != plan.ImportDigest {
				return plan, false, ErrUnsafePath
			}
		case "complete":
			expected, err := initializationCompletionEvidence(layout)
			if err != nil || receipt != expected {
				return plan, false, ErrUnsafePath
			}
			return plan, true, nil
		default:
			data, err := hex.DecodeString(receipt)
			if err != nil || len(data) != 32 {
				return plan, false, ErrUnsafePath
			}
		}
	}
	if plan.ImportDigest != "" {
		if _, err := ReadInitializationInput(layout, plan); err != nil {
			return plan, false, err
		}
	}
	return plan, false, nil
}

func initializationStep(step string) bool {
	switch step {
	case "control", "custody-attempt", "custody", "traffic", "administrator", "ca", "certificate", "import", "complete":
		return true
	}
	return false
}

func readInitializationFile(path string) ([]byte, error) {
	if err := ValidateOwnerOnlyFile(path); err != nil {
		return nil, err
	}
	file, err := openInitializationFile(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil {
		err = validateOwnerOnlyFile(info, path)
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 16385))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > 16384 {
		return nil, ErrUnsafePath
	}
	return data, nil
}

func BeginInitialization(owner *Ownership, plan InitializationPlan) error {
	return beginInitialization(owner, plan, initializationPublication{})
}

func beginInitialization(owner *Ownership, plan InitializationPlan, publication initializationPublication) error {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return err
	}
	handle, err := os.Open(layout.Root)
	if err != nil {
		return err
	}
	entries, readErr := handle.ReadDir(2)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, handle.Close()); err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != LockName {
		return ErrUnsafePath
	}
	directory := filepath.Join(layout.Root, InitializationName)
	directoryID, err := initializationRootIdentity(layout.Root)
	if err != nil {
		return err
	}
	data, err := json.Marshal(initializationDocument{Plan: plan, DirectoryID: directoryID})
	if err != nil {
		return err
	}
	canonicalPlanRoot, rootErr := filepath.EvalSymlinks(plan.Root)
	if rootErr != nil || canonicalPlanRoot != layout.Root || !filepath.IsAbs(plan.Root) || filepath.Clean(plan.Root) != plan.Root || len(data) > 16384 || plan.Version != 1 || !contract.ValidAuditID(plan.Installation) || !contract.ValidAuditID(plan.Traffic) || !filepath.IsAbs(plan.Bearer) || filepath.Clean(plan.Bearer) != plan.Bearer || len(plan.Bearer) > 4096 {
		return ErrUnsafePath
	}
	if plan.ImportDigest != "" && !validInitializationDigest(plan.ImportDigest) {
		return ErrUnsafePath
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	if err := publication.after("plan:directory-create"); err != nil {
		return err
	}
	return writeInitializationFile(directory, "plan", data, publication)
}

func InitializationReceipt(layout Layout, step string) (string, error) {
	if !initializationStep(step) {
		return "", ErrUnsafePath
	}
	data, err := readInitializationFile(filepath.Join(layout.Root, InitializationName, step))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

// SealInitialization records only verified completed work. Exclusive publication
// retains partial files on failure; a later invocation refuses malformed evidence.
func SealInitialization(owner *Ownership, step, evidence string) error {
	return sealInitialization(owner, step, evidence, initializationPublication{})
}

func sealInitialization(owner *Ownership, step, evidence string, publication initializationPublication) error {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return err
	}
	if !initializationStep(step) || evidence == "" || len(evidence) > 16384 {
		return ErrUnsafePath
	}
	if step == "complete" {
		if evidence != "complete" {
			return ErrUnsafePath
		}
		evidence, err = initializationCompletionEvidence(layout)
		if err != nil {
			return err
		}
	}
	old, err := InitializationReceipt(layout, step)
	if err != nil {
		return err
	}
	if old != "" {
		if old != evidence {
			return ErrUnsafePath
		}
		return syncInitializationFile(layout, step, publication)
	}
	return writeInitializationFile(filepath.Join(layout.Root, InitializationName), step, []byte(evidence), publication)
}

func initializationCompletionEvidence(layout Layout) (string, error) {
	hash := sha256.New()
	names := []string{"plan", "control", "custody-attempt", "custody", "traffic", "administrator", "ca", "certificate"}
	planData, err := readInitializationFile(filepath.Join(layout.Root, InitializationName, "plan"))
	if err != nil {
		return "", err
	}
	var document initializationDocument
	if err := json.Unmarshal(planData, &document); err != nil {
		return "", err
	}
	if document.Plan.ImportDigest != "" {
		names = append(names, "import")
	}
	for _, name := range names {
		data, err := readInitializationFile(filepath.Join(layout.Root, InitializationName, name))
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%s:%d:", name, len(data))
		_, _ = hash.Write(data)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// ConfirmInitialization reseals a recognized plan before any domain effect.
// A readable file after an uncertain sync is not yet a durable consent record.
func ConfirmInitialization(owner *Ownership, expected InitializationPlan) error {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return err
	}
	actual, complete, err := ReadInitialization(layout)
	if err != nil {
		return err
	}
	if complete || actual != expected {
		return ErrUnsafePath
	}
	if err := syncInitializationFile(layout, "plan", initializationPublication{}); err != nil {
		return err
	}
	if expected.ImportDigest != "" {
		return syncInitializationFile(layout, "input", initializationPublication{})
	}
	return nil
}

type initializationPublication struct {
	fault func(string) error
	write func(*os.File, []byte) (int, error)
}

func (publication initializationPublication) after(point string) error {
	if publication.fault != nil {
		return publication.fault(point)
	}
	return nil
}

func syncInitializationFile(layout Layout, name string, publication initializationPublication) error {
	directory := filepath.Join(layout.Root, InitializationName)
	file, err := openInitializationFile(filepath.Join(directory, name))
	if err != nil {
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := publication.after(name + ":file-sync"); err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	if err := publication.after(name + ":directory-sync"); err != nil {
		return err
	}
	if err := syncDirectory(layout.Root); err != nil {
		return err
	}
	return publication.after(name + ":root-sync")
}

func writeInitializationFile(directory, name string, data []byte, publication initializationPublication) error {
	limit := 16384
	if name == "input" {
		limit = contract.ConfigExportMaxBytes
	}
	if len(data) == 0 || len(data) > limit {
		return ErrUnsafePath
	}
	file, err := CreateOwnerOnlyFile(filepath.Join(directory, name))
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := publication.after(name + ":create"); err != nil {
		return err
	}
	write := publication.write
	if write == nil {
		write = (*os.File).Write
	}
	n, err := write(file, data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	if err := publication.after(name + ":write"); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := publication.after(name + ":file-sync"); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := publication.after(name + ":close"); err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	if err := publication.after(name + ":directory-sync"); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(directory)); err != nil {
		return err
	}
	return publication.after(name + ":root-sync")
}

func RequireInitializationComplete(layout Layout) error {
	_, err := os.Lstat(filepath.Join(layout.Root, InitializationName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} // Existing pre-protocol installations remain supported.
	if err != nil {
		return err
	}
	_, complete, err := ReadInitialization(layout)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInitializationIncomplete, err)
	}
	if !complete {
		return ErrInitializationIncomplete
	}
	return nil
}
