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

func openTrafficStage(ctx context.Context, ownership *gatewaypaths.Ownership, installation, generation string, config TrafficConfig, create bool, fault func(string) error, populate func(context.Context, *sql.DB) error) (*TrafficStore, error) {
	if ownership == nil || !validOpaqueInvocationID(installation) || !validOpaqueInvocationID(generation) || !config.valid() {
		return nil, ErrInvalidInput
	}
	layout, err := ownership.ActiveLayout()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(layout.Root, "traffic-"+generation+".db")
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
		_, err = db.ExecContext(ctx, storage.TrafficSchema())
		if err == nil {
			_, err = db.ExecContext(ctx, `INSERT INTO traffic_meta VALUES(1,?,?,0,0,0,0)`, installation, generation)
		}
		if err == nil {
			_, err = db.ExecContext(ctx, fmt.Sprintf(`PRAGMA application_id=%d; PRAGMA user_version=1`, trafficApplicationID))
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
	db, err := trafficDatabase(ctx, path, config, false, false)
	if err != nil {
		return nil, err
	}
	s := &TrafficStore{db: db, path: path, config: config, pins: make(map[*TrafficReceipt]*trafficPin),
		admissions: make(chan *trafficRequest, config.QueueRecords), terminals: make(chan *trafficRequest, config.QueueRecords),
		stop: make(chan struct{}), done: make(chan struct{}), readSlots: make(chan struct{}, config.Readers), fault: fault}
	if err = s.validateTraffic(ctx, installation, generation); err != nil {
		_ = db.Close()
		return nil, err
	}
	readers, err := trafficDatabase(ctx, path, config, true, false)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s.readerDB = readers
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
				err = gatewaypaths.ValidateOwnerOnlyFile(path + suffix)
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
	settings := []string{"busy_timeout(50)", "foreign_keys(1)", "synchronous(full)", "cache_spill(0)", "wal_autocheckpoint(0)", "journal_size_limit(0)", "temp_store(memory)"}
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
	db.SetMaxIdleConns(connections)
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func trafficFiles(path string, c TrafficConfig) error {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		candidate := path + suffix
		if suffix != "" {
			if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		if err := gatewaypaths.ValidateOwnerOnlyFile(candidate); err != nil {
			return err
		}
		info, err := os.Stat(candidate)
		if err != nil {
			return err
		}
		if suffix == "-shm" {
			if info.Size() > 16<<20 {
				return ErrInvalidState
			}
			continue
		}
		total += info.Size()
		if suffix == "" && info.Size() > trafficPages(c)*trafficPageSize {
			return ErrInvalidState
		}
	}
	if total > c.BudgetBytes {
		return ErrInvalidState
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
	// A pinned backup may outlive the write deadline. Refuse before mutation
	// rather than waiting for its snapshot or faulting otherwise healthy traffic.
	if !s.readGate.TryLock() {
		return ErrTrafficCapacity
	}
	defer s.readGate.Unlock()
	if ctx.Err() != nil {
		return ErrTrafficDeadline
	}
	if err = trafficCheckpoint(ctx, s.db); err != nil {
		return err
	}
	wal, err = size()
	if err != nil {
		return err
	}
	if wal+trafficReservation(s.config) > maximum {
		return ErrTrafficCapacity
	}
	return nil
}

func (s *TrafficStore) validateTraffic(ctx context.Context, installation, generation string) error {
	var app, version, pageSize, maxPages, syncMode, busy, spill, auto, foreign int64
	var journal, integrity string
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
	if app != trafficApplicationID || version != 1 || pageSize != trafficPageSize || maxPages != trafficPages(s.config) || syncMode != 2 || busy != 50 || spill != 0 || auto != 0 || foreign != 1 || journal != "wal" {
		return ErrInvalidState
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return ErrInvalidState
	}
	return s.validateTrafficEvidence(ctx, installation, generation)
}

func (s *TrafficStore) validateTrafficEvidence(ctx context.Context, installation, generation string) error {
	expected := map[string]bool{}
	for _, ddl := range strings.Split(strings.TrimSpace(storage.TrafficSchema()), "\n\n") {
		expected[strings.Join(strings.Fields(strings.TrimSuffix(strings.TrimSpace(ddl), ";")), " ")] = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' LIMIT 16`)
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
			err = ErrInvalidState
			break
		}
		delete(expected, key)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if len(expected) != 0 {
		return ErrInvalidState
	}
	var storedInstallation, storedGeneration string
	var count, bytes, high, pruning int64
	if err = s.db.QueryRowContext(ctx, `SELECT installation,generation,records,bytes,high_water,pruning FROM traffic_meta WHERE singleton=1`).Scan(&storedInstallation, &storedGeneration, &count, &bytes, &high, &pruning); err != nil {
		return err
	}
	if installation != storedInstallation || generation != storedGeneration || count > s.config.RetainedRecords || bytes > s.retentionBytes() || high < 0 || pruning < 0 || pruning > high {
		return ErrInvalidState
	}
	validationSelect := strings.Replace(invocationSelect, "FROM invocations", ", (SELECT bytes FROM traffic_sizes WHERE id=invocations.id) FROM invocations", 1)
	rows, err = s.db.QueryContext(ctx, validationSelect+` ORDER BY insertion_sequence LIMIT ?`, s.config.RetainedRecords+1)
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
			err = ErrInvalidState
			break
		}
		envelope, details, _ := storedEvidence(record)
		p := PreparedAdmission{Identity: envelope.Identity, admission: Admission{Admission: envelope.Admission, MCP: details}}
		charge := trafficCharge(p)
		if storedCharge != charge {
			err = ErrInvalidState
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
	if actualCount != count || actualBytes != bytes {
		return ErrInvalidState
	}
	var sizeCount int64
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM traffic_sizes LIMIT ?)`, s.config.RetainedRecords+1).Scan(&sizeCount); err != nil {
		return err
	}
	if sizeCount != count {
		return ErrInvalidState
	}
	var sequence int64
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='invocations'),0)`).Scan(&sequence); err != nil {
		return err
	}
	if sequence != high || high-count != pruning {
		return ErrInvalidState
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
