package contract

// TrafficIncident is bounded process-local evidence, not a durable audit or a
// receipt for any individual observation. Counts refer to submissions, not rows.
type TrafficIncident struct {
	FirstFailure  string `json:"first_failure"`
	Cause         string `json:"cause"`
	Stage         string `json:"stage"`
	Settlement    string `json:"settlement"`
	Recovery      string `json:"recovery"`
	RecoveryCause string `json:"recovery_cause"`
	RecoveryStage string `json:"recovery_stage"`
	SQLiteCode    int    `json:"sqlite_code"`
	Affected      uint64 `json:"affected"`
	Discarded     uint64 `json:"discarded"`
}
