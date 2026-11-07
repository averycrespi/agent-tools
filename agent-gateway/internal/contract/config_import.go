package contract

const (
	ConfigPreviewPath = "/api/v2/config/preview"
	ConfigImportPath  = "/api/v2/config/import"
)

// ConfigPreview contains only bounded public keys and closed requirement facts,
// never authored settings, secret availability, or a reservation token.
type ConfigPreview struct {
	Advisory  bool             `json:"advisory"`
	Creations []ConfigCreation `json:"creations"`
}
type ConfigCreation struct {
	Kind         string            `json:"kind"`
	Key          string            `json:"key,omitempty"`
	Reserved     bool              `json:"reserved"`
	Dependencies []ConfigReference `json:"dependencies"`
	Requirements []string          `json:"requirements"`
}
type ConfigImportResult struct {
	Configuration string          `json:"configuration"`
	Creations     []ConfigCreated `json:"creations"`
	Activation    string          `json:"activation"`
}
type ConfigCreated struct {
	Kind     string `json:"kind"`
	Key      string `json:"key,omitempty"`
	ID       string `json:"id,omitempty"`
	Reserved bool   `json:"reserved"`
}
