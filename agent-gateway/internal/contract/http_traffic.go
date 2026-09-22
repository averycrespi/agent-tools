package contract

const (
	HTTPTrafficAdmissionBytes  = 65536
	HTTPTrafficCompletionBytes = 512
	HTTPTrafficGrantFacts      = 4
)

// HTTPTrafficGrant is admission-time policy configuration, not an observed URL.
// It has no foreign key to editable policy and is never reconstructed on reads.
type HTTPTrafficGrant struct {
	Reference HTTPRevisionRef `json:"reference"`
	Policy    HTTPPolicy      `json:"policy"`
}

type HTTPTrafficTarget struct {
	Host   string `json:"host"`
	Port   uint16 `json:"port"`
	Scheme string `json:"scheme,omitempty"`
	Method string `json:"method,omitempty"`
}

type HTTPTrafficMaterial struct {
	Credential HTTPRevisionRef `json:"credential"`
	Generation string          `json:"generation"`
}

// HTTPTrafficAdmission deliberately cannot carry observed paths, query, headers,
// bodies, addresses, raw errors, bearer values or successful response content.
type HTTPTrafficAdmission struct {
	ID                    string               `json:"id"`
	AdmittedAt            string               `json:"admitted_at"`
	Principal             HTTPRevisionRef      `json:"principal"`
	AgentCredential       HTTPRevisionRef      `json:"agent_credential"`
	CredentialFingerprint string               `json:"credential_fingerprint"`
	Class                 string               `json:"class"`
	Default               HTTPDefault          `json:"default"`
	Target                *HTTPTrafficTarget   `json:"target"`
	EvaluatedAt           string               `json:"evaluated_at"`
	Decision              *HTTPDecision        `json:"decision"`
	Grants                []HTTPTrafficGrant   `json:"grants"`
	Material              *HTTPTrafficMaterial `json:"material"`
}

// HTTPTrafficCompletion is closed Gateway evidence, never a transport error.
type HTTPTrafficCompletion struct {
	CompletedAt   string `json:"completed_at"`
	Outcome       string `json:"outcome"`
	Status        int    `json:"status,omitempty"`
	BytesSent     int64  `json:"bytes_sent"`
	BytesReceived int64  `json:"bytes_received"`
	DurationMS    int64  `json:"duration_ms"`
}

type HTTPTrafficFilters struct {
	PrincipalID string `json:"principal_id,omitempty"`
	Destination string `json:"destination,omitempty"`
	Type        string `json:"type,omitempty"`
	Decision    string `json:"decision,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
}

type HTTPTrafficQuery struct {
	Filters HTTPTrafficFilters
	Limit   int
	Cursor  string
}

type HTTPTrafficSummary struct {
	ID          string             `json:"id"`
	AdmittedAt  string             `json:"admitted_at"`
	PrincipalID string             `json:"principal_id"`
	Target      *HTTPTrafficTarget `json:"target"`
	Type        string             `json:"type"`
	Decision    string             `json:"decision"`
	Outcome     string             `json:"outcome"`
}

type HTTPTrafficPage struct {
	Items      []HTTPTrafficSummary `json:"items"`
	NextCursor *string              `json:"next_cursor"`
}

type HTTPTrafficRecord struct {
	Admission  HTTPTrafficAdmission   `json:"admission"`
	Completion *HTTPTrafficCompletion `json:"completion"`
	Sequence   int64                  `json:"-"`
}
