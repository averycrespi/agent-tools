package sandbox

import (
	"bytes"
	"embed"
	"fmt"
	"os/user"
	"strconv"
	"text/template"
)

//go:embed files/lima.yaml
var limaTemplate embed.FS

// TemplateParams are the parameters for rendering the Lima template.
type TemplateParams struct {
	Image    string
	CPUs     int
	Memory   string
	Disk     string
	Username string
	UID      int
	GID      int
	HomeDir  string
	Mounts   []string
}

// HostTemplateParams returns TemplateParams populated from the current host user.
func HostTemplateParams() (TemplateParams, error) {
	u, err := user.Current()
	if err != nil {
		return TemplateParams{}, fmt.Errorf("failed to get current user: %w", err)
	}

	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return TemplateParams{}, fmt.Errorf("failed to parse UID %q: %w", u.Uid, err)
	}

	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return TemplateParams{}, fmt.Errorf("failed to parse GID %q: %w", u.Gid, err)
	}

	return TemplateParams{
		Username: u.Username,
		UID:      uid,
		GID:      gid,
		HomeDir:  u.HomeDir,
	}, nil
}

// RenderTemplate renders the embedded Lima YAML template with the given params.
func RenderTemplate(params TemplateParams) (string, error) {
	data, err := limaTemplate.ReadFile("files/lima.yaml")
	if err != nil {
		return "", fmt.Errorf("failed to read embedded template: %w", err)
	}

	tmpl, err := template.New("lima").Parse(string(data))
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return "", fmt.Errorf("failed to render template: %w", err)
	}

	return buf.String(), nil
}
