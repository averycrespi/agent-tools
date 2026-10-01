package contract

// Recorded activity is process-local acknowledgment evidence, never history or
// execution accounting. Fixed fields exclude identities and arbitrary labels.
const (
	RecordedActivityBuckets         = 60
	RecordedActivityWindow          = 15
	RecordedActivityMaxCount uint64 = 1<<53 - 1
)

type RecordedAdmissions struct {
	Allow                    uint64 `json:"allow"`
	Deny                     uint64 `json:"deny"`
	Block                    uint64 `json:"block"`
	InvalidParams            uint64 `json:"invalid_params"`
	UnknownTool              uint64 `json:"unknown_tool"`
	InvalidArguments         uint64 `json:"invalid_arguments"`
	AuthorizationUnavailable uint64 `json:"authorization_unavailable"`
	InvalidRequest           uint64 `json:"invalid_request"`
	InterceptionSelected     uint64 `json:"interception_selected"`
}

type RecordedCompletions struct {
	Succeeded         uint64 `json:"succeeded"`
	PrestartFailure   uint64 `json:"prestart_failure"`
	DownstreamFailure uint64 `json:"downstream_failure"`
	UpstreamFailure   uint64 `json:"upstream_failure"`
	OutcomeUnknown    uint64 `json:"outcome_unknown"`
}

type RecordedEvents struct {
	Admissions  RecordedAdmissions  `json:"admissions"`
	Completions RecordedCompletions `json:"completions"`
}

type RecordedProtocols struct {
	MCP              RecordedEvents `json:"mcp"`
	HTTPRequest      RecordedEvents `json:"http_request"`
	Connect          RecordedEvents `json:"connect"`
	HTTPUnclassified RecordedEvents `json:"http_unclassified"`
}

type RecordedActivityBucket struct {
	Start         string             `json:"start"`
	End           string             `json:"end"`
	ObservedStart *string            `json:"observed_start"`
	Coverage      string             `json:"coverage"`
	Counts        *RecordedProtocols `json:"counts"`
}

type RecordedActivitySummary struct {
	Epoch           string                   `json:"epoch"`
	CollectionStart string                   `json:"collection_start"`
	AsOf            string                   `json:"as_of"`
	WindowStart     string                   `json:"window_start"`
	WindowEnd       string                   `json:"window_end"`
	BucketSeconds   int                      `json:"bucket_seconds"`
	Coverage        string                   `json:"coverage"`
	EpochReason     string                   `json:"epoch_reason"`
	Buckets         []RecordedActivityBucket `json:"buckets"`
}
