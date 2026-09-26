package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

var ErrInspectionUnavailable = errors.New("read-only inspection cannot establish a closed generation")
var ErrPlanChanged = errors.New("the confirmed maintenance plan changed")

// OperationError reports effects at an owner's actual write/selection boundary.
type OperationError struct {
	Effect string
	Cause  error
}

func (e *OperationError) Error() string {
	return "maintenance operation: " + e.Effect + ": " + e.Cause.Error()
}
func (e *OperationError) Unwrap() error { return e.Cause }

type StoppedApproval func(context.Context, *gatewaypaths.Ownership) error

func ApproveStopped(ctx context.Context, owner *gatewaypaths.Ownership, approvals []StoppedApproval) error {
	for _, approve := range approvals {
		if approve != nil {
			if err := approve(ctx, owner); err != nil {
				return err
			}
		}
	}
	return nil
}

// Inspection contains safe facts only. Its seal binds the generation and exact
// marker slots, not merely a revision (some recovery actions do not advance it).
type Inspection struct {
	Identity Identity `json:"identity"`
	Marked   bool     `json:"recovery_marked"`
	Recovery string   `json:"recovery_action,omitempty"`
	seal     [32]byte
}

// RequireClosedGeneration refuses immutable reads that could hide committed WAL
// or rollback-journal content. It never checkpoints, opens a writer, or repairs.
func RequireClosedGeneration(path string) error {
	for _, suffix := range []string{"-wal", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.Join(ErrInspectionUnavailable, err)
		}
		if err := gatewaypaths.ValidateOwnerOnlyFile(path + suffix); err != nil {
			return err
		}
		if info.Size() != 0 {
			return ErrInspectionUnavailable
		}
	}
	return nil
}

// InspectMaintenance runs domain-owned read queries against an immutable closed
// database under existing stopped ownership. It creates no SQLite sidecars,
// installation files, audit records or recovery markers.
func InspectMaintenance(ctx context.Context, owner *gatewaypaths.Ownership, view func(*sql.Tx) error) (Inspection, error) {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return Inspection{}, err
	}
	return InspectClosedInstallation(ctx, layout, view)
}

// InspectClosedInstallation can observe an idle running installation without
// writing. It refuses WAL-bearing state; only stopped ownership plus Revalidate
// can authorize a mutation from these facts.
func InspectClosedInstallation(ctx context.Context, layout gatewaypaths.Layout, view func(*sql.Tx) error) (Inspection, error) {
	if err := gatewaypaths.InspectRoot(layout.Root); err != nil {
		return Inspection{}, err
	}
	if err := RequireClosedGeneration(layout.Database); err != nil {
		return Inspection{}, err
	}
	identity, err := VerifyBackup(ctx, layout.Database)
	if err != nil {
		return Inspection{}, err
	}
	result := Inspection{Identity: identity}
	hash := sha256.New()
	file, err := os.Open(layout.Database)
	if err != nil {
		return result, err
	}
	count, readErr := io.Copy(hash, io.LimitReader(file, (1<<30)+1))
	if err = errors.Join(readErr, file.Close()); err != nil {
		return result, err
	}
	if count > 1<<30 {
		return result, ErrInvalidDatabase
	}
	marker := newMutationMarker(layout, nil)
	var action *recoveryAction
	for index, path := range marker.paths() {
		_, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			_, _ = fmt.Fprintf(hash, "marker:%d:absent;", index)
			continue
		}
		if err != nil {
			return result, err
		}
		if err = gatewaypaths.ValidateOwnerOnlyFile(path); err != nil {
			return result, err
		}
		file, err := os.Open(path)
		if err != nil {
			return result, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, markerBytesMaximum+1))
		if err = errors.Join(readErr, file.Close()); err != nil {
			return result, err
		}
		var document markerDocument
		if strictjson.Decode(data, &document, strictjson.Options{MaxBytes: markerBytesMaximum, MaxDepth: 4, RejectUnknownMembers: true}) != nil || document.State != "armed" || document.InstallationID != identity.InstallationID {
			return result, ErrStorageLatched
		}
		result.Marked = true
		if document.Recovery != nil {
			if !validRecoveryAction(*document.Recovery) || action != nil && *action != *document.Recovery {
				return result, ErrStorageLatched
			}
			action = document.Recovery
			result.Recovery = action.Action
		}
		_, _ = fmt.Fprintf(hash, "marker:%d:%d:", index, len(data))
		_, _ = hash.Write(data)
	}
	copy(result.seal[:], hash.Sum(nil))
	if view != nil {
		uri := &url.URL{Scheme: "file", Path: layout.Database, RawQuery: "mode=ro&immutable=1"}
		db, err := sql.Open("sqlite3", uri.String())
		if err != nil {
			return result, err
		}
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err == nil {
			err = view(tx)
			err = errors.Join(err, tx.Rollback())
		}
		err = errors.Join(err, db.Close())
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func (expected Inspection) Revalidate(ctx context.Context, owner *gatewaypaths.Ownership) error {
	current, err := InspectMaintenance(ctx, owner, nil)
	if err != nil {
		return err
	}
	if current != expected {
		return ErrPlanChanged
	}
	return nil
}
