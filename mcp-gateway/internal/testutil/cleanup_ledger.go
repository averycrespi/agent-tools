package testutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

const CleanupLedgerEnvironment = "MCP_GATEWAY_TEST_CLEANUP_LEDGER"

const (
	ledgerGracePeriod = 10 * time.Second
	ledgerKillPeriod  = time.Second
)

type CleanupLedger struct {
	path string
}

type cleanupLedgerRecord struct {
	PID      int    `json:"pid"`
	GroupID  int    `json:"group_id"`
	Identity string `json:"identity"`
	Active   bool   `json:"active"`
}

type CleanupSurvivor struct {
	PID     int
	GroupID int
}

type cleanupRegistration struct {
	ledger *CleanupLedger
	record cleanupLedgerRecord
}

func NewCleanupLedger(directory string) (*CleanupLedger, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve cleanup ledger directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create cleanup ledger directory: %w", err)
	}
	path := filepath.Join(absolute, "process-groups.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create cleanup ledger: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close cleanup ledger: %w", err)
	}
	return &CleanupLedger{path: path}, nil
}

func OpenCleanupLedger(path string) (*CleanupLedger, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("cleanup ledger path must be absolute")
	}
	info, err := os.Lstat(path) //nolint:gosec // The inherited absolute path is opened only after rejecting non-regular files.
	if err != nil {
		return nil, fmt.Errorf("inspect cleanup ledger: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("cleanup ledger must be a regular file")
	}
	return &CleanupLedger{path: path}, nil
}

func (ledger *CleanupLedger) Path() string { return ledger.path }

func cleanupLedgerFromEnvironment() (*CleanupLedger, error) {
	path := os.Getenv(CleanupLedgerEnvironment)
	if path == "" {
		return nil, nil
	}
	return OpenCleanupLedger(path)
}

func (ledger *CleanupLedger) register(pid, groupID int) (*cleanupRegistration, error) {
	identity, err := externalProcessIdentity(pid)
	if err != nil {
		return nil, fmt.Errorf("capture process identity: %w", err)
	}
	record := cleanupLedgerRecord{PID: pid, GroupID: groupID, Identity: identity, Active: true}
	if err := ledger.append(record); err != nil {
		return nil, err
	}
	return &cleanupRegistration{ledger: ledger, record: record}, nil
}

func (registration *cleanupRegistration) settle() {
	if registration == nil {
		return
	}
	record := registration.record
	record.Active = false
	_ = registration.ledger.append(record)
}

func (ledger *CleanupLedger) append(record cleanupLedgerRecord) error {
	contents, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode cleanup ledger record: %w", err)
	}
	contents = append(contents, '\n')
	file, err := os.OpenFile(ledger.path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open cleanup ledger: %w", err)
	}
	_, writeErr := file.Write(contents)
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("append cleanup ledger: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close cleanup ledger: %w", closeErr)
	}
	return nil
}

func (ledger *CleanupLedger) activeRecords() ([]cleanupLedgerRecord, error) {
	file, err := os.Open(ledger.path)
	if err != nil {
		return nil, fmt.Errorf("open cleanup ledger: %w", err)
	}
	defer func() { _ = file.Close() }()
	latest := make(map[string]cleanupLedgerRecord)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		var record cleanupLedgerRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("decode cleanup ledger: %w", err)
		}
		if record.PID <= 0 || record.GroupID <= 0 || record.Identity == "" {
			return nil, fmt.Errorf("cleanup ledger contains invalid process identity")
		}
		latest[fmt.Sprintf("%d:%s", record.PID, record.Identity)] = record
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read cleanup ledger: %w", err)
	}
	active := make([]cleanupLedgerRecord, 0, len(latest))
	for _, record := range latest {
		if record.Active && revalidateExternalProcessGroup(record.PID, record.GroupID, record.Identity) {
			active = append(active, record)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].PID < active[j].PID })
	return active, nil
}

func (ledger *CleanupLedger) Cleanup() error {
	active, err := ledger.activeRecords()
	if err != nil {
		return err
	}
	for _, record := range active {
		if revalidateExternalProcessGroup(record.PID, record.GroupID, record.Identity) {
			_ = signalExternalProcessGroup(record.GroupID, syscall.SIGTERM)
		}
	}
	waitForLedgerGroups(active, ledgerGracePeriod)
	for _, record := range active {
		if revalidateExternalProcessGroup(record.PID, record.GroupID, record.Identity) {
			_ = signalExternalProcessGroup(record.GroupID, syscall.SIGKILL)
		}
	}
	waitForLedgerGroups(active, ledgerKillPeriod)
	survivors := ledger.Survivors()
	if len(survivors) > 0 {
		return fmt.Errorf("cleanup ledger retained %d process group survivor(s)", len(survivors))
	}
	return nil
}

func waitForLedgerGroups(records []cleanupLedgerRecord, timeout time.Duration) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive := false
		for _, record := range records {
			alive = alive || revalidateExternalProcessGroup(record.PID, record.GroupID, record.Identity)
		}
		if !alive {
			return
		}
		select {
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}

func (ledger *CleanupLedger) Survivors() []CleanupSurvivor {
	records, err := ledger.activeRecords()
	if err != nil {
		return []CleanupSurvivor{{PID: -1, GroupID: -1}}
	}
	survivors := make([]CleanupSurvivor, 0, len(records))
	for _, record := range records {
		if revalidateExternalProcessGroup(record.PID, record.GroupID, record.Identity) {
			survivors = append(survivors, CleanupSurvivor{PID: record.PID, GroupID: record.GroupID})
		}
	}
	return survivors
}

func CleanupInheritedProcesses() error {
	ledger, err := cleanupLedgerFromEnvironment()
	if err != nil || ledger == nil {
		return err
	}
	return ledger.Cleanup()
}
