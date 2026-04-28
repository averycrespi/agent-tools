package private

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubRunner struct {
	out []byte
	err error
	got struct {
		ctx  context.Context
		name string
		args []string
	}
}

func (s *stubRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	s.got.ctx, s.got.name, s.got.args = ctx, name, args
	return s.out, s.err
}

func TestFetcher_Info_StreamsFile(t *testing.T) {
	// Arrange: write a fake .info file that go mod download would have produced.
	tmp := t.TempDir()
	infoPath := filepath.Join(tmp, "v1.2.3.info")
	require.NoError(t, os.WriteFile(infoPath, []byte(`{"Version":"v1.2.3"}`), 0o600))

	runner := &stubRunner{
		out: []byte(`{"Info":"` + infoPath + `","GoMod":"/x","Zip":"/y","Version":"v1.2.3"}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Version: "v1.2.3", Artifact: ArtifactInfo}
	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"Version":"v1.2.3"`)
	assert.Equal(t, "go", runner.got.name)
	assert.Equal(t, []string{"mod", "download", "-json", "github.com/foo/bar@v1.2.3"}, runner.got.args)
}

func TestFetcher_List_ReturnsPlaintext(t *testing.T) {
	runner := &stubRunner{
		out: []byte(`{"Path":"github.com/foo/bar","Versions":["v1.0.0","v1.1.0"]}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Artifact: ArtifactList}
	w := httptest.NewRecorder()
	require.NoError(t, f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "v1.0.0\nv1.1.0\n", w.Body.String())
	assert.Equal(t, []string{"list", "-m", "-json", "-versions", "github.com/foo/bar@latest"}, runner.got.args)
}

func TestFetcher_Latest_ReturnsInfoJSON(t *testing.T) {
	runner := &stubRunner{
		out: []byte(`{"Path":"github.com/foo/bar","Version":"v1.1.0","Time":"2024-01-01T00:00:00Z"}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Artifact: ArtifactLatest}
	w := httptest.NewRecorder()
	require.NoError(t, f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"Version":"v1.1.0"`)
}

func TestFetcher_PropagatesToolError(t *testing.T) {
	runner := &stubRunner{err: assertErr{}, out: []byte("go: no such module")}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Version: "v1.2.3", Artifact: ArtifactInfo}
	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	assert.Error(t, err)
}

func TestFetcher_Info_FileMissing(t *testing.T) {
	// Runner returns valid JSON but with a path that does not exist on disk.
	runner := &stubRunner{
		out: []byte(`{"Info":"/nonexistent/path/v1.2.3.info","GoMod":"/x","Zip":"/y","Version":"v1.2.3"}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Version: "v1.2.3", Artifact: ArtifactInfo}
	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	assert.Error(t, err)
}

func TestFetcher_ReportsDownloadError(t *testing.T) {
	// Runner exits cleanly but JSON contains an Error field.
	runner := &stubRunner{
		out: []byte(`{"Error":"go: no such module"}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Version: "v1.2.3", Artifact: ArtifactInfo}
	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "go: no such module")
}

func TestFetcher_Info_MalformedJSON(t *testing.T) {
	// Runner exits cleanly but returns invalid JSON.
	runner := &stubRunner{out: []byte("not-json")}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Version: "v1.2.3", Artifact: ArtifactInfo}
	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing go mod download output")
}

func TestFetcher_Info_NotFound_ReportedError(t *testing.T) {
	// Command exits cleanly, JSON Error field signals missing version.
	runner := &stubRunner{
		out: []byte(`{"Error":"github.com/foo/bar@v99.99.99: invalid version: unknown revision v99.99.99"}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Version: "v99.99.99", Artifact: ArtifactInfo}
	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrModuleNotFound)
}

func TestFetcher_Info_NotFound_CommandFailed(t *testing.T) {
	// Command exits non-zero AND emits JSON with Error field — prefers JSON.
	runner := &stubRunner{
		err: assertErr{},
		out: []byte(`{"Error":"reading https://proxy.golang.org/...: 404 Not Found"}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Version: "v1.2.3", Artifact: ArtifactInfo}
	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrModuleNotFound)
}

func TestFetcher_Info_NotClassifiedAsNotFound(t *testing.T) {
	// Auth failure must NOT be classified as not-found.
	runner := &stubRunner{
		err: assertErr{},
		out: []byte(`could not read Username for 'https://github.com': terminal prompts disabled`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Version: "v1.2.3", Artifact: ArtifactInfo}
	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrModuleNotFound)
}

func TestFetcher_List_NotFound(t *testing.T) {
	runner := &stubRunner{
		out: []byte(`{"Path":"github.com/foo/ghost","Error":"repository does not exist"}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/ghost", Artifact: ArtifactList}
	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrModuleNotFound)
}

func TestFetcher_Latest_NotFound(t *testing.T) {
	runner := &stubRunner{
		err: assertErr{},
		out: []byte(`{"Error":"no matching versions for query \"latest\""}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Artifact: ArtifactLatest}
	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrModuleNotFound)
}

type assertErr struct{}

func (assertErr) Error() string { return "boom" }

// failingWriter accepts header writes but errors on every body Write,
// simulating a mid-stream failure (disk I/O error or client disconnect).
type failingWriter struct {
	hdr  http.Header
	code int
}

func (f *failingWriter) Header() http.Header {
	if f.hdr == nil {
		f.hdr = http.Header{}
	}
	return f.hdr
}
func (f *failingWriter) WriteHeader(c int)         { f.code = c }
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestFetcher_StreamFile_ResponseCommitted(t *testing.T) {
	// Runner points at a real on-disk .info file so os.Open succeeds and
	// headers get written; io.Copy then fails on the failing writer. The
	// returned error must satisfy ErrResponseCommitted so the server skips
	// http.Error and avoids a superfluous WriteHeader.
	tmp := t.TempDir()
	infoPath := filepath.Join(tmp, "v1.2.3.info")
	require.NoError(t, os.WriteFile(infoPath, []byte(`{"Version":"v1.2.3"}`), 0o600))

	runner := &stubRunner{
		out: []byte(`{"Info":"` + infoPath + `","GoMod":"/x","Zip":"/y","Version":"v1.2.3"}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Version: "v1.2.3", Artifact: ArtifactInfo}
	err := f.Serve(&failingWriter{}, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResponseCommitted)
}

func TestFetcher_List_ResponseCommitted(t *testing.T) {
	runner := &stubRunner{
		out: []byte(`{"Versions":["v1.0.0","v1.1.0"]}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Artifact: ArtifactList}
	err := f.Serve(&failingWriter{}, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResponseCommitted)
}

func TestFetcher_PropagatesRequestContext(t *testing.T) {
	// The HTTP request's context must reach the runner so cancellation
	// (client disconnect, server shutdown) terminates the subprocess.
	tmp := t.TempDir()
	infoPath := filepath.Join(tmp, "v1.2.3.info")
	require.NoError(t, os.WriteFile(infoPath, []byte(`{"Version":"v1.2.3"}`), 0o600))

	runner := &stubRunner{
		out: []byte(`{"Info":"` + infoPath + `","GoMod":"/x","Zip":"/y","Version":"v1.2.3"}`),
	}
	f := New(runner)

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "sentinel")
	httpReq := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	req := Request{Module: "github.com/foo/bar", Version: "v1.2.3", Artifact: ArtifactInfo}
	require.NoError(t, f.Serve(httptest.NewRecorder(), httpReq, req))

	require.NotNil(t, runner.got.ctx)
	assert.Equal(t, "sentinel", runner.got.ctx.Value(ctxKey{}))
}

func TestFetcher_Latest_ResponseCommitted(t *testing.T) {
	runner := &stubRunner{
		out: []byte(`{"Version":"v1.1.0","Time":"2024-01-01T00:00:00Z"}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Artifact: ArtifactLatest}
	err := f.Serve(&failingWriter{}, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResponseCommitted)
}
