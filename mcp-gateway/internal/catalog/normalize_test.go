package catalog

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/gowebpki/jcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeToolProjectsClosedFieldsMaterializesDefaultsAndFingerprintsCanonically(t *testing.T) {
	left := rawTool(`{"unknown":"discard","name":"echo","description":"desc","inputSchema":{"properties":{"value":{"type":"string"}},"type":"object"},"icons":[{}],"annotations":{"unknown":true}}`)
	right := rawTool(`{"annotations":{"openWorldHint":true,"idempotentHint":false,"destructiveHint":true,"readOnlyHint":false},"inputSchema":{"type":"object","properties":{"value":{"type":"string"}}},"description":"desc","name":"echo"}`)
	leftNormalized, err := NormalizeTool(left, NormalizeOptions{ServerID: "server"})
	require.NoError(t, err)
	rightNormalized, err := NormalizeTool(right, NormalizeOptions{ServerID: "server"})
	require.NoError(t, err)
	assert.Equal(t, leftNormalized.Canonical, rightNormalized.Canonical)
	assert.Equal(t, leftNormalized.Fingerprint, rightNormalized.Fingerprint)
	assert.Equal(t, CandidateKey{ServerID: "server", UpstreamName: "echo"}, leftNormalized.Key)
	assert.Equal(t, "sample.echo", leftNormalized.ExternalName)
	assert.Nil(t, leftNormalized.Descriptor.Title)
	assert.Equal(t, contract.NormalizedToolAnnotations{DestructiveHint: true, OpenWorldHint: true}, leftNormalized.Descriptor.Annotations)
	assert.NotContains(t, string(leftNormalized.Canonical), "unknown")
	assert.NotContains(t, string(leftNormalized.Canonical), "icons")
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), leftNormalized.Fingerprint)
	assert.JSONEq(t, `{"annotations":{"title":null,"readOnlyHint":false,"destructiveHint":true,"idempotentHint":false,"openWorldHint":true},"description":"desc","inputSchema":{"properties":{"value":{"type":"string"}},"type":"object"},"name":"echo"}`, string(leftNormalized.Canonical))
}

func TestRFC8785CanonicalizationCoversNumbersUnicodeAndKeyOrder(t *testing.T) {
	canonical, err := jcs.Transform([]byte(`{"numbers":[333333333.33333329,1E30,4.50,2e-3,0.000000000000000000000000001],"string":"€$\u000f\nA'B\"\\\"/","literals":[null,true,false]}`))
	require.NoError(t, err)
	assert.Equal(t, `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\"/"}`, string(canonical))
}

func TestNormalizeSchemaCompilesDraft2020AndAllowsOnlyLocalReferences(t *testing.T) {
	valid := []string{
		`{"type":"object","$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"name":{"type":"string"}},"properties":{"name":{"$ref":"#/$defs/name"}}}`,
		`{"type":"object","properties":{},"additionalProperties":false}`,
	}
	for _, schema := range valid {
		_, _, err := normalizeSchema(json.RawMessage(schema), false)
		require.NoError(t, err)
	}
	invalid := []string{
		`{"type":"string"}`,
		`{"properties":{}}`,
		`{"type":"object","$schema":"http://json-schema.org/draft-07/schema#"}`,
		`{"type":"object","properties":{"x":{"$ref":"https://attacker.example/schema"}}}`,
		`{"type":"object","properties":{"x":{"$dynamicRef":"#node"}}}`,
		`{"type":"object","properties":{"x":{"type":"string","pattern":"["}}}`,
	}
	for _, schema := range invalid {
		_, _, err := normalizeSchema(json.RawMessage(schema), false)
		assert.ErrorIs(t, err, ErrDescriptorInvalid, schema)
	}
}

func TestModernHTTPHeaderBindingsAreNestedTypedUniqueAndSorted(t *testing.T) {
	tool := rawTool(`{"name":"echo","inputSchema":{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"X-Region"},"nested":{"type":"object","properties":{"count":{"type":"integer","x-mcp-header":"X-Count"},"enabled":{"type":"boolean","x-mcp-header":"X-Enabled"}}}}}}`)
	normalized, err := NormalizeTool(tool, NormalizeOptions{ServerID: "server", AllowHeaderBindings: true})
	require.NoError(t, err)
	assert.Equal(t, []HeaderBinding{
		{Path: []string{"nested", "count"}, Header: "X-Count", Kind: HeaderInteger},
		{Path: []string{"nested", "enabled"}, Header: "X-Enabled", Kind: HeaderBoolean},
		{Path: []string{"region"}, Header: "X-Region", Kind: HeaderString},
	}, normalized.HeaderBindings)
	assert.Contains(t, string(normalized.Descriptor.InputSchema), `"x-mcp-header":"X-Region"`)

	invalidSchemas := []string{
		`{"type":"object","x-mcp-header":"X-Root"}`,
		`{"type":"object","properties":{"x":{"type":"number","x-mcp-header":"X-Value"}}}`,
		`{"type":"object","properties":{"x":{"type":"string","x-mcp-header":"Bad Header"}}}`,
		`{"type":"object","properties":{"a":{"type":"string","x-mcp-header":"X-Dupe"},"b":{"type":"string","x-mcp-header":"x-dupe"}}}`,
		`{"type":"object","items":{"type":"string","x-mcp-header":"X-Item"}}`,
	}
	for _, schema := range invalidSchemas {
		invalid := rawTool(`{"name":"echo","inputSchema":` + schema + `}`)
		_, err := NormalizeTool(invalid, NormalizeOptions{ServerID: "server", AllowHeaderBindings: true})
		assert.ErrorIs(t, err, ErrDescriptorInvalid, schema)
	}
	_, err = NormalizeTool(tool, NormalizeOptions{ServerID: "server", AllowHeaderBindings: false})
	assert.ErrorIs(t, err, ErrDescriptorInvalid)
	output := rawTool(`{"name":"echo","inputSchema":{"type":"object"},"outputSchema":{"type":"object","properties":{"x":{"type":"string","x-mcp-header":"X-Output"}}}}`)
	_, err = NormalizeTool(output, NormalizeOptions{ServerID: "server", AllowHeaderBindings: true})
	assert.ErrorIs(t, err, ErrDescriptorInvalid)
}

func TestMirrorHeadersUsesCanonicalScalarAndSEP2243Encoding(t *testing.T) {
	bindings := []HeaderBinding{
		{Path: []string{"plain"}, Header: "Plain", Kind: HeaderString},
		{Path: []string{"unicode"}, Header: "Unicode", Kind: HeaderString},
		{Path: []string{"space"}, Header: "Space", Kind: HeaderString},
		{Path: []string{"sentinel"}, Header: "Sentinel", Kind: HeaderString},
		{Path: []string{"nested", "flag"}, Header: "Flag", Kind: HeaderBoolean},
		{Path: []string{"count"}, Header: "Count", Kind: HeaderInteger},
		{Path: []string{"missing"}, Header: "Missing", Kind: HeaderString},
		{Path: []string{"null"}, Header: "Null", Kind: HeaderString},
	}
	headers, err := MirrorHeaders(bindings, map[string]any{"plain": "us-west1", "unicode": "日本語", "space": " leading", "sentinel": "=?base64?literal?=", "nested": map[string]any{"flag": true}, "count": json.Number("42"), "null": nil})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"Plain": "us-west1", "Unicode": "=?base64?5pel5pys6Kqe?=", "Space": "=?base64?IGxlYWRpbmc=?=", "Sentinel": "=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?=", "Flag": "true", "Count": "42",
	}, headers)
	_, err = MirrorHeaders([]HeaderBinding{{Path: []string{"count"}, Header: "Count", Kind: HeaderInteger}}, map[string]any{"count": json.Number("9007199254740992")})
	assert.ErrorIs(t, err, ErrDescriptorInvalid)
	_, err = MirrorHeaders([]HeaderBinding{{Path: []string{"flag"}, Header: "Flag", Kind: HeaderBoolean}}, map[string]any{"flag": "true"})
	assert.ErrorIs(t, err, ErrDescriptorInvalid)
}

func TestNormalizeToolEnforcesTextSchemaAndCanonicalSizeBounds(t *testing.T) {
	validTitle := strings.Repeat("t", int(fixedLimit("tool_title_bytes")))
	validDescription := strings.Repeat("d", int(fixedLimit("tool_description_bytes")))
	tool := rawTool(marshalDescriptor(t, map[string]any{"name": "echo", "title": validTitle, "description": validDescription, "inputSchema": map[string]any{"type": "object"}}))
	_, err := NormalizeTool(tool, NormalizeOptions{ServerID: "server"})
	require.NoError(t, err)
	for _, descriptor := range []string{
		marshalDescriptor(t, map[string]any{"name": "echo", "title": validTitle + "x", "inputSchema": map[string]any{"type": "object"}}),
		marshalDescriptor(t, map[string]any{"name": "echo", "description": validDescription + "x", "inputSchema": map[string]any{"type": "object"}}),
	} {
		_, err := NormalizeTool(rawTool(descriptor), NormalizeOptions{ServerID: "server"})
		assert.ErrorIs(t, err, ErrDescriptorInvalid)
	}
	atSchemaLimit := sizedSchema(t, int(fixedLimit("tool_schema_bytes")))
	_, err = NormalizeTool(rawTool(`{"name":"echo","inputSchema":`+atSchemaLimit+`}`), NormalizeOptions{ServerID: "server"})
	require.NoError(t, err)
	overSchemaLimit := sizedSchema(t, int(fixedLimit("tool_schema_bytes"))+1)
	_, err = NormalizeTool(rawTool(`{"name":"echo","inputSchema":`+overSchemaLimit+`}`), NormalizeOptions{ServerID: "server"})
	assert.ErrorIs(t, err, ErrDescriptorInvalid)
}

func TestNormalizeCandidateExcludesInvalidSiblingAndPreservesStableKeysAcrossEras(t *testing.T) {
	candidate := Candidate{Tools: []RawTool{rawTool(`{"name":"one","inputSchema":{"type":"object"}}`), rawTool(`{"name":"bad","inputSchema":{"type":"string"}}`)}, Issues: []IssueClass{IssueDescriptorInvalid}, RawCount: 3, Pages: 1}
	for _, allowHeaders := range []bool{false, true} {
		normalized := NormalizeCandidate(candidate, NormalizeOptions{ServerID: "server", AllowHeaderBindings: allowHeaders})
		assert.Len(t, normalized.Tools, 1)
		assert.Len(t, normalized.Issues, 2)
		assert.Equal(t, CandidateKey{ServerID: "server", UpstreamName: "one"}, normalized.Tools[0].Key)
	}
}

func rawTool(descriptor string) RawTool {
	var object map[string]json.RawMessage
	_ = json.Unmarshal([]byte(descriptor), &object)
	var name string
	_ = json.Unmarshal(object["name"], &name)
	return RawTool{UpstreamName: name, ExternalName: "sample." + name, Descriptor: json.RawMessage(descriptor)}
}

func sizedSchema(t *testing.T, size int) string {
	t.Helper()
	prefix, suffix := `{"type":"object","description":"`, `"}`
	require.GreaterOrEqual(t, size, len(prefix)+len(suffix))
	return prefix + strings.Repeat("s", size-len(prefix)-len(suffix)) + suffix
}

func marshalDescriptor(t *testing.T, value any) string {
	t.Helper()
	contents, err := json.Marshal(value)
	require.NoError(t, err)
	return string(contents)
}
