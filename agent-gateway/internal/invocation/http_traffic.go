package invocation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

func (s *TrafficStore) ObserveHTTP(admission contract.HTTPTrafficAdmission) *TrafficObservation {
	encoded, err := encodeHTTPAdmission(admission)
	if err != nil {
		s.drop()
		return nil
	}
	observation := &TrafficObservation{prepared: PreparedAdmission{Identity: activity.Identity{InvocationID: admission.ID, AdmittedAt: admission.AdmittedAt}}, httpAdmission: encoded, recorded: httpRecordedAdmission(admission), bytes: httpTrafficChargeBase + int64(len(encoded))}
	s.observeInitial(observation)
	return observation
}

func (s *TrafficStore) ObserveHTTPCompletion(observation *TrafficObservation, completion contract.HTTPTrafficCompletion) error {
	var admission contract.HTTPTrafficAdmission
	if observation == nil || observation.httpAdmission == "" || observation.gitAdmission != "" || strictjson.Decode([]byte(observation.httpAdmission), &admission, strictjson.Options{MaxBytes: contract.HTTPTrafficAdmissionBytes, MaxDepth: 12, RejectUnknownMembers: true}) != nil {
		s.drop()
		return ErrInvalidInput
	}
	encoded, err := encodeHTTPCompletion(admission, completion)
	if err != nil {
		s.drop()
		return err
	}
	return s.enqueueObservation(&trafficRequest{observation: *observation, httpCompletion: encoded, recorded: recordedTerminal(observation.recorded.protocol, completion.Outcome), bytes: observation.bytes + maxTrafficCompletionBytes})
}

func (r *trafficRequest) terminal() bool {
	return r.completion != nil || r.httpCompletion != "" || r.gitCompletion != ""
}

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
	err = s.view(ctx, func(ctx context.Context, tx *sql.Tx) error {
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
