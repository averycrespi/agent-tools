package format

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatAuthor_Regular(t *testing.T) {
	got := FormatAuthor(Author{Login: "daviddl9"})
	assert.Equal(t, "@daviddl9", got)
}

func TestFormatAuthor_Bot(t *testing.T) {
	got := FormatAuthor(Author{Login: "dependabot", IsBot: true})
	assert.Equal(t, "@dependabot [bot]", got)
}

func TestFormatAuthor_Empty(t *testing.T) {
	got := FormatAuthor(Author{})
	assert.Equal(t, "@unknown", got)
}

func TestFormatDate_Full(t *testing.T) {
	got := FormatDate("2025-02-09T10:26:21Z")
	assert.Equal(t, "2025-02-09", got)
}

func TestFormatDate_Empty(t *testing.T) {
	got := FormatDate("")
	assert.Equal(t, "", got)
}

func TestFormatDate_Short(t *testing.T) {
	got := FormatDate("bad")
	assert.Equal(t, "bad", got)
}

func TestTruncateBody_Short(t *testing.T) {
	got := TruncateBody("hello", 100)
	assert.Equal(t, "hello", got)
}

func TestTruncateBody_Exact(t *testing.T) {
	got := TruncateBody("hello", 5)
	assert.Equal(t, "hello", got)
}

func TestTruncateBody_Long(t *testing.T) {
	got := TruncateBody("word1 word2 word3 word4 word5", 15)
	assert.Contains(t, got, "word1 word2")
	assert.Contains(t, got, "[truncated")
	assert.NotContains(t, got, "word5")
}

func TestTruncateBody_Zero(t *testing.T) {
	got := TruncateBody("anything", 0)
	assert.Equal(t, "", got)
}

func TestStripImages(t *testing.T) {
	assert.Equal(t, "[image]", StripImages("![alt text](http://example.com/img.png)"))
	assert.Equal(t, "before [image] after", StripImages("before ![alt](url) after"))
	assert.Equal(t, "no images here", StripImages("no images here"))
}

func TestFormatLabels(t *testing.T) {
	got := FormatLabels([]Label{{Name: "bug"}, {Name: "enhancement"}})
	assert.Equal(t, "bug, enhancement", got)
}

func TestFormatLabels_Empty(t *testing.T) {
	got := FormatLabels(nil)
	assert.Equal(t, "(none)", got)
}
