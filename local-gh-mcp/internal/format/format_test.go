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

func TestParseDiffSummary_TwoFiles(t *testing.T) {
	diff := `diff --git a/foo.go b/foo.go
index 1234567..abcdefg 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 package foo
+import "fmt"
+func hello() { fmt.Println("hi") }
-func old() {}
diff --git a/bar.go b/bar.go
index 1234567..abcdefg 100644
--- a/bar.go
+++ b/bar.go
@@ -1,2 +1,3 @@
 package bar
+func New() {}
`
	got := ParseDiffSummary(diff)
	assert.Contains(t, got, "## Files changed (2)")
	assert.Contains(t, got, "foo.go")
	assert.Contains(t, got, "bar.go")
	assert.Contains(t, got, "+2 -1") // foo.go: 2 added, 1 removed
	assert.Contains(t, got, "+1 -0") // bar.go: 1 added, 0 removed
}

func TestParseDiffSummary_Empty(t *testing.T) {
	got := ParseDiffSummary("")
	assert.Equal(t, "", got)
}

func TestFormatDiff(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,2 +1,3 @@
 package main
+func init() {}
`
	got := FormatDiff(diff, 0)
	assert.Contains(t, got, "## Files changed")
	assert.Contains(t, got, "## Diff")
	assert.Contains(t, got, diff)
}

func TestFormatDiff_TruncatesOnLineBoundary(t *testing.T) {
	diff := "diff --git a/foo b/foo\n--- a/foo\n+++ b/foo\n@@ -1,1 +1,2 @@\n line1\n+addedAAAAAAAAAAAAAAAAAAAA\n+addedBBBBBBBBBBBBBBBBBBBB\n"
	got := FormatDiff(diff, 80)
	// Summary table built from the full diff regardless of cap.
	assert.Contains(t, got, "## Files changed (1)")
	assert.Contains(t, got, "foo")
	// Trailer reports both shown bytes and total.
	assert.Contains(t, got, "[truncated")
	assert.Contains(t, got, "/")
}

func TestFormatDiff_NoCapWhenUnderLimit(t *testing.T) {
	diff := "diff --git a/foo b/foo\n@@ -1 +1 @@\n-old\n+new\n"
	got := FormatDiff(diff, 10000)
	assert.NotContains(t, got, "[truncated")
}
