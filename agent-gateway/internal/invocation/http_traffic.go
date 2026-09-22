package invocation

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

func (s *TrafficStore) AdmitHTTP(ctx context.Context, admission contract.HTTPTrafficAdmission) (*TrafficReceipt, error) {
	encoded, err := encodeHTTPAdmission(admission)
	if err != nil {
		return nil, err
	}
	return s.enqueueAdmission(&trafficRequest{ctx: ctx, prepared: PreparedAdmission{Identity: activity.Identity{InvocationID: admission.ID, AdmittedAt: admission.AdmittedAt}}, httpAdmission: encoded, httpAllowed: admission.Decision != nil && admission.Decision.Allowed, bytes: httpTrafficChargeBase + int64(len(encoded)), expires: time.Now().Add(s.config.QueueLifetime), result: make(chan trafficResult, 1)})
}

func (s *TrafficStore) CompleteHTTP(ctx context.Context, receipt *TrafficReceipt, completion contract.HTTPTrafficCompletion) error {
	var admission contract.HTTPTrafficAdmission
	valid := receipt != nil && receipt.httpAdmission != ""
	if valid {
		valid = strictjson.Decode([]byte(receipt.httpAdmission), &admission, strictjson.Options{MaxBytes: contract.HTTPTrafficAdmissionBytes, MaxDepth: 12, RejectUnknownMembers: true}) == nil
	}
	encoded, err := encodeHTTPCompletion(admission, completion)
	return s.enqueueCompletion(&trafficRequest{ctx: ctx, receipt: receipt, httpCompletion: encoded, bytes: maxTrafficCompletionBytes, expires: time.Now().Add(s.config.QueueLifetime), result: make(chan trafficResult, 1)}, valid && err == nil)
}

func (r *trafficRequest) terminal() bool { return r.completion != nil || r.httpCompletion != "" }

type HTTPTrafficHistory struct {
	Generation string
	HighWater  int64
	Pruning    int64
	Records    []contract.HTTPTrafficRecord
}

func (s *TrafficStore) HTTPHistory(ctx context.Context, after int64, limit int) (result HTTPTrafficHistory, err error) {
	if after < 0 || limit < 1 || limit > 256 {
		return result, ErrInvalidInput
	}
	err = s.view(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT generation,high_water,pruning FROM traffic_meta WHERE singleton=1`).Scan(&result.Generation, &result.HighWater, &result.Pruning); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, httpTrafficSelect+` WHERE insertion_sequence>? ORDER BY insertion_sequence LIMIT ?`, after, limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			record, _, scanErr := scanHTTPTraffic(rows)
			if scanErr != nil {
				err = scanErr
				break
			}
			result.Records = append(result.Records, record)
		}
		return errors.Join(err, rows.Err(), rows.Close())
	})
	if err != nil {
		return HTTPTrafficHistory{}, err
	}
	return result, nil
}
