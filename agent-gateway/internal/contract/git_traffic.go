package contract

const (
	GitTrafficAdmissionBytes  = 8192
	GitTrafficCompletionBytes = 512
)

// GitTrafficAdmission is deliberately a minimal privacy-safe receipt: no wire
// prefix, request binding, ref names, object IDs, URLs, messages or pack content.
type GitTrafficAdmission struct {
	ID                    string              `json:"id"`
	AdmittedAt            string              `json:"admitted_at"`
	EvaluatedAt           string              `json:"evaluated_at"`
	Principal             GitRevisionRef      `json:"principal"`
	AgentCredential       GitRevisionRef      `json:"agent_credential"`
	Repository            GitRevisionRef      `json:"repository"`
	AliasRevision         string              `json:"alias_revision"`
	ProfileRevision       string              `json:"profile_revision"`
	AuthorizationRevision string              `json:"authorization_revision"`
	Operation             string              `json:"operation"`
	Commands              int                 `json:"commands"`
	Allowed               bool                `json:"allowed"`
	Material              *GitTrafficMaterial `json:"material,omitempty"`
	PrivateGrant          *HTTPRevisionRef    `json:"private_grant,omitempty"`
}
type GitTrafficMaterial struct {
	Credential GitRevisionRef `json:"credential"`
	Generation string         `json:"generation"`
}

// A clean transfer is not a Git mutation result. Rich protocol observations have
// a separate owner; HTTP status never changes outcome_unknown into push success.
type GitTrafficCompletion struct {
	CompletedAt      string `json:"completed_at"`
	Outcome          string `json:"outcome"`
	Status           int    `json:"status,omitempty"`
	BytesSent        int64  `json:"bytes_sent"`
	BytesReceived    int64  `json:"bytes_received"`
	DurationMS       int64  `json:"duration_ms"`
	TransferComplete bool   `json:"transfer_complete"`
}
type GitTrafficRecord struct {
	Admission  GitTrafficAdmission   `json:"admission"`
	Completion *GitTrafficCompletion `json:"completion"`
	Sequence   int64                 `json:"-"`
}
