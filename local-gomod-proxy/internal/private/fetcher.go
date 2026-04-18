package private

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/exec"
)

// Fetcher serves private module artifacts by invoking the host's go toolchain.
type Fetcher struct {
	runner exec.Runner
}

// New returns a Fetcher that shells out via runner.
func New(runner exec.Runner) *Fetcher {
	return &Fetcher{runner: runner}
}

type downloadResult struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
	Info    string `json:"Info"`
	GoMod   string `json:"GoMod"`
	Zip     string `json:"Zip"`
	Error   string `json:"Error"`
}

type listResult struct {
	Path     string   `json:"Path"`
	Version  string   `json:"Version"`
	Time     string   `json:"Time"`
	Versions []string `json:"Versions"`
	Error    string   `json:"Error"`
}

// Serve handles a single Request.
func (f *Fetcher) Serve(w http.ResponseWriter, _ *http.Request, req Request) error {
	switch req.Artifact {
	case ArtifactInfo, ArtifactMod, ArtifactZip:
		return f.serveArtifact(w, req)
	case ArtifactList:
		return f.serveList(w, req)
	case ArtifactLatest:
		return f.serveLatest(w, req)
	default:
		return fmt.Errorf("unsupported artifact: %d", req.Artifact)
	}
}

func (f *Fetcher) serveArtifact(w http.ResponseWriter, req Request) error {
	out, err := f.runner.Run("go", "mod", "download", "-json", req.Module+"@"+req.Version)
	if err != nil {
		return fmt.Errorf("go mod download: %w: %s", err, out)
	}
	var r downloadResult
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("parsing go mod download output: %w", err)
	}
	if r.Error != "" {
		return fmt.Errorf("go mod download reported: %s", r.Error)
	}
	var path, contentType string
	switch req.Artifact {
	case ArtifactInfo:
		path, contentType = r.Info, "application/json"
	case ArtifactMod:
		path, contentType = r.GoMod, "text/plain; charset=utf-8"
	case ArtifactZip:
		path, contentType = r.Zip, "application/zip"
	}
	return streamFile(w, path, contentType)
}

func (f *Fetcher) serveList(w http.ResponseWriter, req Request) error {
	out, err := f.runner.Run("go", "list", "-m", "-json", "-versions", req.Module+"@latest")
	if err != nil {
		return fmt.Errorf("go list: %w: %s", err, out)
	}
	var r listResult
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("parsing go list output: %w", err)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, strings.Join(r.Versions, "\n"))
	if len(r.Versions) > 0 {
		_, _ = io.WriteString(w, "\n")
	}
	return nil
}

func (f *Fetcher) serveLatest(w http.ResponseWriter, req Request) error {
	out, err := f.runner.Run("go", "list", "-m", "-json", req.Module+"@latest")
	if err != nil {
		return fmt.Errorf("go list: %w: %s", err, out)
	}
	var r listResult
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("parsing go list output: %w", err)
	}
	info := map[string]string{"Version": r.Version, "Time": r.Time}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(info)
}

func streamFile(w http.ResponseWriter, path, contentType string) error {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, err = io.Copy(w, f)
	return err
}
