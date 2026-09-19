//go:build darwin || linux

package service

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceStatusLegacyEncodingWarning(t *testing.T) {
	for _, encoding := range []string{"canonical", "missing-doctype", "paired-booleans", "legacy"} {
		t.Run(encoding, func(t *testing.T) {
			f := newFixture(t)
			f.install(t)
			original, err := os.ReadFile(f.m.plist())
			require.NoError(t, err)
			data := original
			if encoding == "missing-doctype" || encoding == "legacy" {
				data = bytes.ReplaceAll(data, []byte(plistDoctype+"\n"), nil)
			}
			if encoding == "paired-booleans" || encoding == "legacy" {
				data = bytes.ReplaceAll(data, []byte("<true/>"), []byte("<true></true>"))
			}
			require.NoError(t, os.WriteFile(f.m.plist(), data, 0600))
			result, err := f.m.execute(t.Context(), "status", Changes{})
			require.NoError(t, err)
			require.True(t, result.Installed)
			if encoding == "canonical" {
				require.Empty(t, result.Warnings)
			} else {
				require.Len(t, result.Warnings, 1)
				require.Contains(t, result.Warnings[0], "No repair was performed")
				require.Contains(t, result.Warnings[0], "plutil -convert xml1")
			}
			require.Empty(t, f.mutations)
			after, err := os.ReadFile(f.m.plist())
			require.NoError(t, err)
			require.Equal(t, data, after)
			_, err = f.m.execute(t.Context(), "update", Changes{})
			require.NoError(t, err)
			after, err = os.ReadFile(f.m.plist())
			require.NoError(t, err)
			require.Equal(t, data, after)
			require.Empty(t, f.mutations)
		})
	}
}

func TestLegacyEncodingIgnoresEscapedLiteral(t *testing.T) {
	f := newFixture(t)
	f.install(t)
	d, _, _, err := f.m.read()
	require.NoError(t, err)
	d.DataDir = "/data/<true></true>"
	data, err := d.encode()
	require.NoError(t, err)
	require.False(t, legacyPlistEncoding(data))
}
