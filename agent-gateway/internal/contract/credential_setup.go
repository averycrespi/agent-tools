package contract

// CredentialMaterialState is safe setup evidence, not permission to dispatch.
// Zero material revision identifies a definition that has never received material;
// attempted or invalidated generations remain unavailable, not unconfigured.
type CredentialMaterialState string

const (
	CredentialNotConfigured       CredentialMaterialState = "not_configured"
	CredentialMaterialReady       CredentialMaterialState = "ready"
	CredentialMaterialUnavailable CredentialMaterialState = "unavailable"
)

func MaterialState(revision uint64, available bool) CredentialMaterialState {
	if available {
		return CredentialMaterialReady
	}
	if revision == 0 {
		return CredentialNotConfigured
	}
	return CredentialMaterialUnavailable
}

func (state CredentialMaterialState) Valid(available bool) bool {
	return available && state == CredentialMaterialReady || !available && (state == CredentialNotConfigured || state == CredentialMaterialUnavailable)
}

func (state CredentialMaterialState) Label() string {
	switch state {
	case CredentialNotConfigured:
		return "Not configured"
	case CredentialMaterialReady:
		return "Configured"
	default:
		return "Unavailable"
	}
}

// CredentialSetupFact is a focused, nonsecret input for stopped doctor/setup
// consumers. It neither resolves material nor changes doctor presentation.
type CredentialSetupFact struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Action   string   `json:"action"`
	Slots    []string `json:"slots,omitempty"`
	Inactive bool     `json:"inactive,omitempty"`
}

// CredentialSetupStatus reports metadata only, never resolved credentials.
// Unavailable metadata is independent of serving or storage health.
type CredentialSetupStatus struct {
	State string                `json:"state"`
	Facts []CredentialSetupFact `json:"facts"`
}
