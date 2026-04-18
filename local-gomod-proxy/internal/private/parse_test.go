package private

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRequest(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantMod string
		wantVer string
		wantArt Artifact
		wantErr bool
	}{
		{"info", "/github.com/foo/bar/@v/v1.2.3.info", "github.com/foo/bar", "v1.2.3", ArtifactInfo, false},
		{"mod", "/github.com/foo/bar/@v/v1.2.3.mod", "github.com/foo/bar", "v1.2.3", ArtifactMod, false},
		{"zip", "/github.com/foo/bar/@v/v1.2.3.zip", "github.com/foo/bar", "v1.2.3", ArtifactZip, false},
		{"list", "/github.com/foo/bar/@v/list", "github.com/foo/bar", "", ArtifactList, false},
		{"latest", "/github.com/foo/bar/@latest", "github.com/foo/bar", "", ArtifactLatest, false},
		{"escaped uppercase", "/github.com/!foo/bar/@v/v1.0.0.info", "github.com/Foo/bar", "v1.0.0", ArtifactInfo, false},
		{"bad path", "/not-a-module", "", "", 0, true},
		{"bad artifact", "/github.com/foo/bar/@v/v1.0.0.tar", "", "", 0, true},
		{"traversal attempt", "/../@v/list", "", "", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := ParseRequest(tc.path)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantMod, req.Module)
			assert.Equal(t, tc.wantVer, req.Version)
			assert.Equal(t, tc.wantArt, req.Artifact)
		})
	}
}
