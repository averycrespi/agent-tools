package invocation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

const trafficApplicationID = 0x4d475431 // MGT1, not control MGW1
const trafficPageSize int64 = 4096

var trafficOwners = struct {
	sync.Mutex
	paths map[string]bool
}{paths: make(map[string]bool)}

// CreateTraffic creates a new, explicitly named generation under installation
// ownership. It neither selects it nor replaces an existing generation. Failed
// publication retains staging evidence; OpenTraffic never initializes a miss.
func CreateTraffic(ctx context.Context, ownership *gatewaypaths.Ownership, installation, generation string, config TrafficConfig) (*TrafficStore, error) {
	return openTraffic(ctx, ownership, installation, generation, config, true, nil)
}

func OpenTraffic(ctx context.Context, ownership *gatewaypaths.Ownership, installation, generation string, config TrafficConfig) (*TrafficStore, error) {
	return openTraffic(ctx, ownership, installation, generation, config, false, nil)
}

func openTraffic(ctx context.Context, ownership *gatewaypaths.Ownership, installation, generation string, config TrafficConfig, create bool, fault func(string) error) (*TrafficStore, error) {
	return openTrafficStage(ctx, ownership, installation, generation, config, create, fault, nil)
}

func openTrafficStage(ctx context.Context, ownership *gatewaypaths.Ownership, installation, generation string, config TrafficConfig, create bool, fault func(string) error, populate func(context.Context, *sql.DB) error) (result *TrafficStore, resultErr error) {
	if ownership == nil || !validOpaqueInvocationID(installation) || !validOpaqueInvocationID(generation) || !config.valid() {
		return nil, ErrInvalidInput
	}
	layout, err := ownership.ActiveLayout()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(layout.Root, "traffic-"+generation+".db")
	defer func() {
		if resultErr != nil {
			detail := diagnostics.Snapshot("traffic", "open generation", path, resultErr)
			detail.Explanation = "generation=" + generation + "; " + detail.Explanation
			detail.Effect = "history unavailable; preserve rejected artifacts"
			resultErr = diagnostics.WithDetail(resultErr, detail)
		}
	}()
	trafficOwners.Lock()
	if trafficOwners.paths[layout.Root] {
		trafficOwners.Unlock()
		return nil, ErrTrafficCapacity
	}
	trafficOwners.paths[layout.Root] = true
	trafficOwners.Unlock()
	success := false
	defer func() {
		if !success {
			trafficOwners.Lock()
			delete(trafficOwners.paths, layout.Root)
			trafficOwners.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if create {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return nil, ErrInvalidState
		}
		stage := path + ".staging"
		file, err := gatewaypaths.CreateOwnerOnlyFile(stage)
		if err != nil {
			return nil, err
		}
		if err = file.Close(); err != nil {
			return nil, err
		}
		db, err := trafficDatabase(ctx, stage, config, false, true)
		if err != nil {
			return nil, err
		}
		_, err = db.ExecContext(ctx, storage.TrafficSchemaVersion(3))
		if err == nil {
			_, err = db.ExecContext(ctx, `INSERT INTO traffic_meta VALUES(1,?,?,0,0,0,0)`, installation, generation)
		}
		if err == nil {
			_, err = db.ExecContext(ctx, fmt.Sprintf(`PRAGMA application_id=%d; PRAGMA user_version=3`, trafficApplicationID))
		}
		if err == nil && populate != nil {
			err = populate(ctx, db)
		}
		if err == nil {
			err = trafficCheckpoint(ctx, db)
		}
		err = errors.Join(err, db.Close())
		if err != nil {
			return nil, err
		}
		db, err = trafficDatabase(ctx, stage, config, false, false)
		if err != nil {
			return nil, err
		}
		stageStore := &TrafficStore{db: db, path: stage, config: config}
		err = errors.Join(stageStore.validateTraffic(ctx, installation, generation), db.Close())
		if err != nil {
			return nil, err
		}
		file, err = os.OpenFile(stage, os.O_RDWR, 0)
		if err != nil {
			return nil, err
		}
		err = errors.Join(file.Sync(), file.Close())
		if err != nil {
			return nil, err
		}
		// A hard link provides no-replace publication of the synced closed inode.
		if err = os.Link(stage, path); err != nil {
			return nil, err
		}
		if err = trafficSyncDir(layout.Root); err != nil {
			return nil, err
		}
		if err = os.Remove(stage); err != nil {
			return nil, err
		}
		if err = trafficSyncDir(layout.Root); err != nil {
			return nil, err
		}
	}
	if err = trafficFiles(path, config); err != nil {
		return nil, err
	}
	// Authenticate the WAL-aware state using read-only handles before opening
	// any writable handle: last-writer close can checkpoint even rejected WAL.
	readers, err := trafficDatabase(ctx, path, config, true, false)
	if err != nil {
		return nil, err
	}
	s := &TrafficStore{db: readers, path: path, config: config, installation: installation, generation: generation,
		observations: make(chan *trafficRequest, config.QueueRecords),
		stop:         make(chan struct{}), done: make(chan struct{}), readSlots: make(chan struct{}, config.Readers), fault: fault}
	var journal string
	err = readers.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal)
	if err == nil && journal != "wal" {
		err = trafficPredicate("journal_mode", "matches=false expected=wal")
	}
	if err == nil {
		err = s.validateTrafficContents(ctx, installation, generation)
	}
	if err != nil {
		return nil, classifyTraffic(errors.Join(err, readers.Close()), "validation", "not_started")
	}
	db, err := trafficDatabase(ctx, path, config, false, false)
	if err != nil {
		return nil, errors.Join(err, readers.Close())
	}
	s.db = db
	// Uninterrupted exclusive ownership preserves the authenticated contents;
	// the writer still verifies its own connection-local durability settings.
	if err = s.validateTrafficSettings(ctx); err == nil {
		if err = s.upgradeTraffic(ctx); err != nil {
			err = errors.Join(ErrTrafficFault, err)
		}
	} else {
		err = classifyTraffic(err, "validation", "not_started")
	}
	if err != nil {
		return nil, errors.Join(err, db.Close(), readers.Close())
	}
	s.readerDB = readers
	s.recorded = newRecordedActivity()
	success = true
	go s.runTraffic()
	return s, nil
}

func trafficSyncDir(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func trafficPages(c TrafficConfig) int64       { return (c.BudgetBytes - (64 << 10)) / (3 * trafficPageSize) }
func trafficReservation(c TrafficConfig) int64 { return 32 + (trafficPages(c)+2)*(trafficPageSize+24) }

func trafficDatabase(ctx context.Context, path string, c TrafficConfig, read, create bool) (*sql.DB, error) {
	if !read {
		for _, suffix := range []string{"-wal", "-shm"} {
			file, err := gatewaypaths.CreateOwnerOnlyFile(path + suffix)
			if err == nil {
				err = file.Close()
			} else if errors.Is(err, os.ErrExist) {
				err = gatewaypaths.ValidateSQLiteSidecar(path + suffix)
			}
			if err != nil {
				return nil, err
			}
		}
	}
	uri := &url.URL{Scheme: "file", Path: path}
	q := uri.Query()
	q.Set("mode", "rw")
	q.Set("_txlock", "immediate")
	settings := []string{"busy_timeout(50)", "foreign_keys(1)", "synchronous(full)", "cache_spill(0)", "journal_size_limit(0)", "temp_store(memory)"}
	if read {
		q.Set("mode", "ro")
		q.Set("_txlock", "deferred")
		settings = append(settings, "query_only(1)")
	} else {
		settings = append(settings, "max_page_count("+strconv.FormatInt(trafficPages(c), 10)+")")
		if create {
			settings = append(settings, "journal_mode(wal)")
		}
	}
	for _, p := range settings {
		q.Add("_pragma", p)
	}
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return nil, err
	}
	connections := 1
	if read {
		connections = c.Readers
	}
	db.SetMaxOpenConns(connections)
	// Handles belong to bounded operations, not to the process lifetime. SQLite
	// retains its default autocheckpoint and last-connection-close behavior.
	// Installation and transaction settlement ownership are independent of pooling.
	db.SetMaxIdleConns(0)
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func trafficFiles(path string, c TrafficConfig) error {
	// WAL is ordinary SQLite recovery state, not an application intent marker.
	// A rollback journal is not part of the verified WAL-mode traffic format.
	if info, err := os.Lstat(path + "-journal"); err == nil {
		if err := gatewaypaths.ValidateOwnerOnlyFile(path + "-journal"); err != nil {
			return err
		}
		if info.Size() != 0 {
			return trafficPredicate("rollback_journal_bytes", fmt.Sprintf("observed=%d allowed=0", info.Size()))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		candidate := path + suffix
		if suffix != "" {
			if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		validate := gatewaypaths.ValidateOwnerOnlyFile
		if suffix != "" {
			validate = gatewaypaths.ValidateSQLiteSidecar
		}
		if err := validate(candidate); err != nil {
			return err
		}
		info, err := os.Stat(candidate)
		if err != nil {
			return err
		}
		if suffix == "-shm" {
			if info.Size() > 16<<20 {
				return trafficPredicate("shm_bytes", fmt.Sprintf("observed=%d allowed=%d", info.Size(), 16<<20))
			}
			continue
		}
		total += info.Size()
		if suffix == "" && info.Size() > trafficPages(c)*trafficPageSize {
			return trafficPredicate("database_partition_bytes", fmt.Sprintf("observed=%d allowed=%d", info.Size(), trafficPages(c)*trafficPageSize))
		}
	}
	if total > c.BudgetBytes {
		return trafficPredicate("total_file_bytes", fmt.Sprintf("observed=%d allowed=%d", total, c.BudgetBytes))
	}
	return nil
}

func trafficCheckpoint(ctx context.Context, db *sql.DB) error {
	var busy, frames, done int
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &done); err != nil {
		return err
	}
	if busy != 0 || frames != done {
		return ErrTrafficCapacity
	}
	return nil
}

func (s *TrafficStore) reserveTraffic(ctx context.Context) error {
	if ctx.Err() != nil {
		return ErrTrafficDeadline
	}
	s.setPressureReason("none")
	if err := trafficFiles(s.path, s.config); err != nil {
		return err
	}
	size := func() (int64, error) {
		info, err := os.Stat(s.path + "-wal")
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		return info.Size(), nil
	}
	wal, err := size()
	if err != nil {
		return err
	}
	maximum := s.config.BudgetBytes - (64 << 10) - trafficPages(s.config)*trafficPageSize
	if wal+trafficReservation(s.config) <= maximum {
		return nil
	}
	s.setPressureReason("budget_reservation")
	return ErrTrafficCapacity
}

func (s *TrafficStore) setPressureReason(reason string) {
	s.mu.Lock()
	s.pressureReason = reason
	s.mu.Unlock()
}

func (s *TrafficStore) validateTraffic(ctx context.Context, installation, generation string) error {
	if err := s.validateTrafficSettings(ctx); err != nil {
		return err
	}
	return s.validateTrafficContents(ctx, installation, generation)
}

func (s *TrafficStore) validateTrafficSettings(ctx context.Context) error {
	var app, version, pageSize, maxPages, syncMode, busy, spill, auto, foreign int64
	var journal string
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT application_id FROM pragma_application_id),
 (SELECT user_version FROM pragma_user_version),(SELECT page_size FROM pragma_page_size),
 (SELECT max_page_count FROM pragma_max_page_count),(SELECT synchronous FROM pragma_synchronous),
 (SELECT timeout FROM pragma_busy_timeout),(SELECT cache_spill FROM pragma_cache_spill),
 (SELECT foreign_keys FROM pragma_foreign_keys),
 (SELECT journal_mode FROM pragma_journal_mode)`).Scan(&app, &version, &pageSize, &maxPages, &syncMode, &busy, &spill, &foreign, &journal); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_autocheckpoint`).Scan(&auto); err != nil {
		return err
	}
	for _, setting := range []struct {
		name               string
		observed, expected int64
	}{
		{"application_id", app, trafficApplicationID}, {"page_size", pageSize, trafficPageSize}, {"max_page_count", maxPages, trafficPages(s.config)}, {"synchronous", syncMode, 2}, {"busy_timeout", busy, 50}, {"cache_spill", spill, 0}, {"foreign_keys", foreign, 1},
	} {
		if setting.observed != setting.expected {
			return trafficPredicate(setting.name, fmt.Sprintf("observed=%d expected=%d", setting.observed, setting.expected))
		}
	}
	if version < 1 || version > 3 {
		return trafficPredicate("schema_version", fmt.Sprintf("observed=%d allowed=1..3", version))
	}
	if auto <= 0 {
		return trafficPredicate("wal_autocheckpoint", fmt.Sprintf("observed=%d expected=positive", auto))
	}
	if journal != "wal" {
		return trafficPredicate("journal_mode", "matches=false expected=wal")
	}
	return nil
}

func (s *TrafficStore) validateTrafficSchema(ctx context.Context, version int) error {
	ddl := storage.TrafficSchemaVersion(version)
	if ddl == "" {
		return trafficPredicate("schema_version", fmt.Sprintf("observed=%d allowed=1..3", version))
	}
	expected := map[string]bool{}
	for _, ddl := range strings.Split(strings.TrimSpace(ddl), "\n\n") {
		expected[strings.Join(strings.Fields(strings.TrimSuffix(strings.TrimSpace(ddl), ";")), " ")] = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' LIMIT ?`, len(expected)+1)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ddl string
		if err = rows.Scan(&ddl); err != nil {
			break
		}
		key := strings.Join(strings.Fields(ddl), " ")
		if !expected[key] {
			err = trafficPredicate("schema_definition", "matches=false expected=exact_versioned_definition actual_ddl=withheld")
			break
		}
		delete(expected, key)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if len(expected) != 0 {
		return trafficPredicate("schema_completeness", fmt.Sprintf("missing_definitions=%d expected_missing=0", len(expected)))
	}
	return nil
}

func (s *TrafficStore) validateTrafficEvidence(ctx context.Context, installation, generation string) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if err := s.validateTrafficSchema(ctx, version); err != nil {
		return err
	}
	var storedInstallation, storedGeneration string
	var count, bytes, high, pruning int64
	if err := s.db.QueryRowContext(ctx, `SELECT installation,generation,records,bytes,high_water,pruning FROM traffic_meta WHERE singleton=1`).Scan(&storedInstallation, &storedGeneration, &count, &bytes, &high, &pruning); err != nil {
		return err
	}
	if installation != storedInstallation || generation != storedGeneration || count > s.config.RetainedRecords || bytes > s.retentionBytes() || high < 0 || pruning < 0 || pruning > high {
		return trafficPredicate("metadata_binding_and_bounds", fmt.Sprintf("installation_matches=%t generation_matches=%t records=%d allowed_records=%d bytes=%d allowed_bytes=%d high_water=%d pruning=%d expected=bound_identity_and_retention_with_0<=pruning<=high_water", installation == storedInstallation, generation == storedGeneration, count, s.config.RetainedRecords, bytes, s.retentionBytes(), high, pruning))
	}
	validationSelect := strings.Replace(invocationSelect, "FROM invocations", ", (SELECT bytes FROM traffic_sizes WHERE id=invocations.id) FROM invocations", 1)
	rows, err := s.db.QueryContext(ctx, validationSelect+` ORDER BY insertion_sequence LIMIT ?`, s.config.RetainedRecords+1)
	if err != nil {
		return err
	}
	var actualCount, actualBytes, previous int64
	for rows.Next() {
		var storedCharge int64
		record, scanErr := scanInvocation(trafficChargeScanner{rows: rows, charge: &storedCharge})
		if scanErr != nil {
			err = scanErr
			break
		}
		if actualCount >= s.config.RetainedRecords || record.Sequence <= previous || record.Sequence > high || !validStoredInvocation(record) {
			err = trafficPredicate("mcp_row", fmt.Sprintf("ordinal=%d sequence=%d previous=%d high_water=%d allowed_records=%d valid_record=%t expected=valid_record_with_increasing_bounded_sequence", actualCount+1, record.Sequence, previous, high, s.config.RetainedRecords, validStoredInvocation(record)))
			break
		}
		envelope, details, _ := storedEvidence(record)
		p := PreparedAdmission{Identity: envelope.Identity, admission: Admission{Admission: envelope.Admission, MCP: details}}
		charge := trafficCharge(p)
		if storedCharge != charge {
			err = trafficPredicate("mcp_row_charge", fmt.Sprintf("ordinal=%d observed=%d expected=%d", actualCount+1, storedCharge, charge))
			break
		}
		actualCount++
		actualBytes += charge
		previous = record.Sequence
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	mcpCount := actualCount
	if version >= 2 {
		httpCount, httpBytes, err := s.validateHTTPTraffic(ctx, high)
		if err != nil {
			return err
		}
		actualCount += httpCount
		actualBytes += httpBytes
	}
	if version >= 3 {
		gitCount, gitBytes, err := s.validateGitTraffic(ctx, high)
		if err != nil {
			return err
		}
		actualCount += gitCount
		actualBytes += gitBytes
	}
	if actualCount != count || actualBytes != bytes {
		return trafficPredicate("retention_accounting", fmt.Sprintf("observed_records=%d expected_records=%d observed_bytes=%d expected_bytes=%d", actualCount, count, actualBytes, bytes))
	}
	var sizeCount int64
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM traffic_sizes LIMIT ?)`, s.config.RetainedRecords+1).Scan(&sizeCount); err != nil {
		return err
	}
	if sizeCount != mcpCount {
		return trafficPredicate("charge_row_count", fmt.Sprintf("observed=%d expected=%d", sizeCount, mcpCount))
	}
	var sequence int64
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='invocations'),0)`).Scan(&sequence); err != nil {
		return err
	}
	if sequence != high || high-count != pruning {
		return trafficPredicate("sequence_and_pruning", fmt.Sprintf("observed_sequence=%d expected_sequence=%d observed_pruning=%d expected_pruning=%d", sequence, high, pruning, high-count))
	}
	return trafficFiles(s.path, s.config)
}

type trafficChargeScanner struct {
	rows   *sql.Rows
	charge *int64
}

func (scanner trafficChargeScanner) Scan(values ...any) error {
	return scanner.rows.Scan(append(values, scanner.charge)...)
}

func (s *TrafficStore) retentionBytes() int64 { return trafficPages(s.config) * trafficPageSize / 4 }
