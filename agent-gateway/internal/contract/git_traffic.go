package contract

const (
	GitTrafficAdmissionBytes   = 8192
	GitTrafficCompletionBytes  = 512
	GitTrafficRefs             = 8
	GitTrafficRefEvidenceBytes = 1536
)

// GitTrafficAdmission retains bounded configured identity and policy facts,
// including bounded requested refs, never observed URLs, wire prefixes,
// object IDs, messages or packs.
type GitTrafficAdmission struct {
	ID                    string                 `json:"id"`
	AdmittedAt            string                 `json:"admitted_at"`
	EvaluatedAt           string                 `json:"evaluated_at"`
	Principal             GitRevisionRef         `json:"principal"`
	AgentCredential       GitRevisionRef         `json:"agent_credential"`
	Repository            GitRevisionRef         `json:"repository"`
	AliasRevision         string                 `json:"alias_revision"`
	ProfileRevision       string                 `json:"profile_revision"`
	AuthorizationRevision string                 `json:"authorization_revision"`
	RefEvidence           *GitTrafficRefEvidence `json:"ref_evidence,omitempty"`
	Policy                *GitTrafficPolicy      `json:"policy,omitempty"`
	Rejection             string                 `json:"rejection,omitempty"`
	Operation             string                 `json:"operation"`
	Commands              int                    `json:"commands"`
	Denial                string                 `json:"denial,omitempty"`
	Allowed               bool                   `json:"allowed"`
	Material              *GitTrafficMaterial    `json:"material,omitempty"`
	PrivateGrant          *HTTPRevisionRef       `json:"private_grant,omitempty"`
}

// GitTrafficPolicy contains configured admission-time identity and bounded
// applicable policy references, never observed command destinations.
type GitTrafficPolicy struct {
	RepositoryName string           `json:"repository_name"`
	RepositoryURL  string           `json:"repository_url"`
	Grants         []GitRevisionRef `json:"grants"`
	GrantCount     int              `json:"grant_count"`
	Creates        int              `json:"creates"`
	Updates        int              `json:"updates"`
	Deletes        int              `json:"deletes"`
}

// RefEvidence retains a request-order prefix. Missing evidence denotes legacy
// unavailability; truncated evidence never describes unrecorded commands.
type GitTrafficRefEvidence struct {
	State string                   `json:"state"`
	Refs  []GitTrafficRequestedRef `json:"refs"`
}

type GitTrafficRequestedRef struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

type GitTrafficMaterial struct {
	Credential GitRevisionRef `json:"credential"`
	Generation string         `json:"generation"`
}

// A clean transfer is not a Git mutation result. Request-bound protocol reports
// remain separate upstream claims; HTTP status alone never supplies a report.
type GitTrafficCompletion struct {
	CompletedAt      string `json:"completed_at"`
	Outcome          string `json:"outcome"`
	Status           int    `json:"status,omitempty"`
	BytesSent        int64  `json:"bytes_sent"`
	BytesReceived    int64  `json:"bytes_received"`
	DurationMS       int64  `json:"duration_ms"`
	TransferComplete bool   `json:"transfer_complete"`
	ReportedResult   string `json:"reported_result,omitempty"`
	// RefOutcomes aligns with admission ref_evidence.refs: ok/ng are upstream
	// claims only. Absence means unavailable, never inferred from aggregate counts.
	RefOutcomes []string `json:"ref_outcomes,omitempty"`
	Failure     string   `json:"failure,omitempty"`
}
type GitTrafficFilters struct {
	From, Until  string
	Operation    string
	Repository   string
	Admission    string
	Transport    string
	Report       string
	SearchLocale string
}

type GitTrafficQuery struct {
	Cursor  string
	Limit   int
	Filters GitTrafficFilters
}
type GitTrafficPage struct {
	Items      []GitTrafficRecord `json:"items"`
	NextCursor *string            `json:"next_cursor"`
}

type GitTrafficRecord struct {
	Admission  GitTrafficAdmission   `json:"admission"`
	Completion *GitTrafficCompletion `json:"completion"`
	Sequence   int64                 `json:"-"`
}
