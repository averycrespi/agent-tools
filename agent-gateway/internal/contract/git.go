package contract

// Git configuration is a dedicated authority domain, not HTTP request policy.
const (
	GitRepositories         = 256
	GitRepositoryIdentities = 1024
	GitGrants               = 4096
	GitCredentials          = 256
	GitAliases              = 4
	GitRules                = 128
	GitRefBytes             = 1024
	GitPolicyBytes          = 32768
	GitLocatorBytes         = 4096
	GitRequestedRefs        = 128
)

type GitRevisionRef struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

type GitRefSelector struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type GitRefRule struct {
	Ref     GitRefSelector `json:"ref"`
	Actions []string       `json:"actions"`
}

type GitPolicy struct {
	Version int          `json:"version"`
	Read    bool         `json:"read"`
	Refs    []GitRefRule `json:"refs"`
}

type GitRepositoryDefinition struct {
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	Aliases      []string `json:"aliases"`
	CredentialID *string  `json:"credential_id"`
}

type GitRepository struct {
	ID string `json:"id"`
	GitRepositoryDefinition
	Revision      string `json:"revision"`
	AliasRevision string `json:"alias_revision"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type GitCredentialDefinition struct {
	Name   string               `json:"name"`
	Origin string               `json:"origin"`
	Recipe HTTPCredentialRecipe `json:"recipe"`
}

type GitCredential struct {
	ID string `json:"id"`
	GitCredentialDefinition
	Revision   string                   `json:"revision"`
	Available  bool                     `json:"available"`
	References []GitCredentialReference `json:"referencing_repositories"`
	CreatedAt  string                   `json:"created_at"`
	UpdatedAt  string                   `json:"updated_at"`
}

type GitCredentialReference struct {
	ID string `json:"id"`
}

type GitGrant struct {
	ID           string     `json:"id"`
	PrincipalID  string     `json:"principal_id"`
	RepositoryID string     `json:"repository_id"`
	Description  *string    `json:"description"`
	Policy       GitPolicy  `json:"policy"`
	ExpiresAt    *string    `json:"expires_at"`
	Revision     string     `json:"revision"`
	State        GrantState `json:"state"`
	CreatedAt    string     `json:"created_at"`
	UpdatedAt    string     `json:"updated_at"`
}

// Profiles survive repository deletion. Active is read-only: authority owns
// the atomic profile/opaque-tunnel transition fence.
type GitRoutingProfile struct {
	Origins  []string `json:"origins"`
	Revision string   `json:"revision"`
	Active   bool     `json:"active"`
}

type GitDecision struct {
	Allowed               bool           `json:"allowed"`
	Reason                string         `json:"reason"`
	Principal             GitRevisionRef `json:"principal"`
	Repository            GitRevisionRef `json:"repository"`
	AuthorizationRevision string         `json:"authorization_revision"`
}
