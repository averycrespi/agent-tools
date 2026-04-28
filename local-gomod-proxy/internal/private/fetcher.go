package private

import (
	"context"
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

// Serve handles a single Request. The HTTP request's context is threaded
// into every subprocess invocation so client disconnects and server
// shutdown terminate in-flight `go` commands.
func (f *Fetcher) Serve(w http.ResponseWriter, httpReq *http.Request, req Request) error {
	ctx := httpReq.Context()
	switch req.Artifact {
	case ArtifactInfo, ArtifactMod, ArtifactZip:
		return f.serveArtifact(ctx, w, req)
	case ArtifactList:
		return f.serveList(ctx, w, req)
	case ArtifactLatest:
		return f.serveLatest(ctx, w, req)
	default:
		return fmt.Errorf("unsupported artifact: %d", req.Artifact)
	}
}

func (f *Fetcher) serveArtifact(ctx context.Context, w http.ResponseWriter, req Request) error {
	out, err := f.runner.Run(ctx, "go", "mod", "download", "-json", req.Module+"@"+req.Version)
	if err != nil {
		return wrapRunError("go mod download", out, err)
	}
	var r downloadResult
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("parsing go mod download output: %w", err)
	}
	if r.Error != "" {
		return classifyError(fmt.Errorf("go mod download reported: %s", r.Error), r.Error)
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

func (f *Fetcher) serveList(ctx context.Context, w http.ResponseWriter, req Request) error {
	out, err := f.runner.Run(ctx, "go", "list", "-m", "-json", "-versions", req.Module+"@latest")
	if err != nil {
		return wrapRunError("go list", out, err)
	}
	var r listResult
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("parsing go list output: %w", err)
	}
	if r.Error != "" {
		return classifyError(fmt.Errorf("go list reported: %s", r.Error), r.Error)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	body := strings.Join(r.Versions, "\n")
	if len(r.Versions) > 0 {
		body += "\n"
	}
	if _, err := io.WriteString(w, body); err != nil {
		return fmt.Errorf("%w: writing list body: %w", ErrResponseCommitted, err)
	}
	return nil
}

func (f *Fetcher) serveLatest(ctx context.Context, w http.ResponseWriter, req Request) error {
	out, err := f.runner.Run(ctx, "go", "list", "-m", "-json", req.Module+"@latest")
	if err != nil {
		return wrapRunError("go list", out, err)
	}
	var r listResult
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("parsing go list output: %w", err)
	}
	if r.Error != "" {
		return classifyError(fmt.Errorf("go list reported: %s", r.Error), r.Error)
	}
	info := map[string]string{"Version": r.Version, "Time": r.Time}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(info); err != nil {
		return fmt.Errorf("%w: encoding latest info: %w", ErrResponseCommitted, err)
	}
	return nil
}

// wrapRunError classifies a failed `go` invocation. When the tool emitted
// structured JSON with an Error field, we prefer that for classification
// (it's the authoritative reason). Otherwise we classify against the raw
// combined output.
func wrapRunError(stage string, out []byte, runErr error) error {
	trimmed := strings.TrimSpace(string(out))
	msg := trimmed
	var e struct {
		Error string `json:"Error"`
	}
	if json.Unmarshal(out, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	return classifyError(fmt.Errorf("%s: %w: %s", stage, runErr, trimmed), msg)
}

func streamFile(w http.ResponseWriter, path, contentType string) error {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("%w: streaming %s: %w", ErrResponseCommitted, path, err)
	}
	return nil
}
