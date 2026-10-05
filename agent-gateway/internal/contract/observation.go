package contract

// DiagnosticDeliveryStatus counts delivery, never requests or execution. Queue
// bytes are fixed reserved record charges, not raw payload or encoded log sizes.
type DiagnosticDeliveryStatus struct {
	State               string  `json:"state"`
	Epoch               string  `json:"epoch"`
	Accepted            uint64  `json:"accepted"`
	Written             uint64  `json:"written"`
	Dropped             uint64  `json:"dropped"`
	Invalid             uint64  `json:"invalid"`
	WriteFailures       uint64  `json:"write_failures"`
	QueueRecords        int     `json:"queue_records"`
	QueueBytes          int     `json:"queue_bytes"`
	QueueLimit          int     `json:"queue_limit"`
	Writing             bool    `json:"writing"`
	LastSuccessfulWrite *string `json:"last_successful_write"`
	Overflow            bool    `json:"overflow"`
}

// DeliveryCounters cover history submissions, not distinct rows or executions.
// Discarded includes both refused submissions and accepted but unsettled loss.
type DeliveryCounters struct {
	Accepted          uint64 `json:"accepted"`
	Acknowledged      uint64 `json:"acknowledged"`
	Discarded         uint64 `json:"discarded"`
	QueueRecords      int    `json:"queue_records"`
	QueueBytes        int64  `json:"queue_bytes"`
	CompletionRecords int    `json:"completion_records"`
	QueueRecordLimit  int    `json:"queue_record_limit"`
	QueueByteLimit    int64  `json:"queue_byte_limit"`
}

// Results order: succeeded, prestart_failure, failed, unknown, nonmutation.
// Git reports: success, failure, partial; these untrusted upstream facts never
// certify Gateway-observed successful mutation. Latency stages: admission,
// execution, request; disjoint buckets <=1/10/100/1000/10000 ms, then greater.
type ObservedProtocol struct {
	Protocol   string       `json:"protocol"`
	Requests   uint64       `json:"requests"`
	Executions uint64       `json:"executions"`
	Results    [5]uint64    `json:"results"`
	GitReports [3]uint64    `json:"git_reports"`
	Latency    [3][6]uint64 `json:"latency"`
}
type ExecutionObservations struct {
	Epoch     string              `json:"epoch"`
	StartedAt string              `json:"started_at"`
	Coverage  string              `json:"coverage"`
	Overflow  bool                `json:"overflow"`
	Protocols [4]ObservedProtocol `json:"protocols"`
}

type ByteMeasurement struct {
	State string `json:"state"`
	Bytes *int64 `json:"bytes"`
}
