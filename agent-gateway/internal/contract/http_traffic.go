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

// HTTPRejection contains only closed rule names, never offending input.
type HTTPRejection struct {
	Stage  string `json:"stage"`
	Reason string `json:"reason"`
}

func (r HTTPRejection) Valid() bool {
	switch r.Stage {
	case "headers":
		return r.Reason == "invalid_headers" || r.Reason == "trailers_unsupported" || r.Reason == "upgrade_unsupported" || r.Reason == "inner_proxy_authorization"
	case "request_form":
		return r.Reason == "connect_body" || r.Reason == "nested_connect" || r.Reason == "origin_form_required" || r.Reason == "absolute_http_required"
	case "target":
		return r.Reason == "invalid_request_target" || r.Reason == "invalid_connect_target"
	}
	return false
}

// HTTPConnectContext identifies the actual enclosing CONNECT admission. Its
// destination is inherited connection evidence, not a validated inner target.
type HTTPConnectContext struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	Port uint16 `json:"port"`
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
	Rejection             *HTTPRejection       `json:"rejection,omitempty"`
	Connect               *HTTPConnectContext  `json:"connect,omitempty"`
}

// HTTPTrafficCompletion is closed Gateway evidence, never a transport error.
type HTTPTrafficCompletion struct {
	CompletedAt    string `json:"completed_at"`
	Outcome        string `json:"outcome"`
	Status         int    `json:"status,omitempty"`
	BytesSent      int64  `json:"bytes_sent"`
	BytesReceived  int64  `json:"bytes_received"`
	DurationMS     int64  `json:"duration_ms"`
	ResponseSource string `json:"response_source,omitempty"`
	GatewayStatus  int    `json:"gateway_status,omitempty"`
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
	ID             string              `json:"id"`
	AdmittedAt     string              `json:"admitted_at"`
	PrincipalID    string              `json:"principal_id"`
	Target         *HTTPTrafficTarget  `json:"target"`
	Type           string              `json:"type"`
	Decision       string              `json:"decision"`
	Outcome        string              `json:"outcome"`
	Rejection      *HTTPRejection      `json:"rejection,omitempty"`
	Connect        *HTTPConnectContext `json:"connect,omitempty"`
	ResponseSource string              `json:"response_source,omitempty"`
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
