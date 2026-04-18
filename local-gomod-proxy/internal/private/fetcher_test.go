package private

import (
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
		dir  string
		name string
		args []string
	}
}

func (s *stubRunner) Run(name string, args ...string) ([]byte, error) {
	s.got.name, s.got.args = name, args
	return s.out, s.err
}

func (s *stubRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	s.got.dir, s.got.name, s.got.args = dir, name, args
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

type assertErr struct{}

func (assertErr) Error() string { return "boom" }
