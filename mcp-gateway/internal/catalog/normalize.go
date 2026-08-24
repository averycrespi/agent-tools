package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/gowebpki/jcs"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var ErrDescriptorInvalid = errors.New("tool descriptor is invalid")

const schema2020 = "https://json-schema.org/draft/2020-12/schema"

type HeaderValueKind string

const (
	HeaderString  HeaderValueKind = "string"
	HeaderBoolean HeaderValueKind = "boolean"
	HeaderInteger HeaderValueKind = "integer"
)

type HeaderBinding struct {
	Path   []string
	Header string
	Kind   HeaderValueKind
}

type CandidateKey struct {
	ServerID     string
	UpstreamName string
}

type NormalizedTool struct {
	Key            CandidateKey
	ExternalName   string
	Descriptor     contract.NormalizedToolDescriptor
	Canonical      json.RawMessage
	Fingerprint    string
	HeaderBindings []HeaderBinding
}

type NormalizedCandidate struct {
	Tools    []NormalizedTool
	Issues   []IssueClass
	RawCount int64
	Pages    int64
	Bytes    int64
}

type NormalizeOptions struct {
	ServerID            string
	AllowHeaderBindings bool
}

func NormalizeCandidate(candidate Candidate, options NormalizeOptions) NormalizedCandidate {
	result := NormalizedCandidate{Tools: make([]NormalizedTool, 0, len(candidate.Tools)), Issues: append([]IssueClass(nil), candidate.Issues...), RawCount: candidate.RawCount, Pages: candidate.Pages, Bytes: candidate.Bytes}
	for _, raw := range candidate.Tools {
		normalized, err := NormalizeTool(raw, options)
		if err != nil {
			result.Issues = append(result.Issues, IssueDescriptorInvalid)
			continue
		}
		result.Tools = append(result.Tools, normalized)
	}
	return result
}

func NormalizeTool(tool RawTool, options NormalizeOptions) (NormalizedTool, error) {
	if options.ServerID == "" || tool.UpstreamName == "" || tool.ExternalName == "" || !utf8.Valid(tool.Descriptor) || int64(len(tool.Descriptor)) > fixedLimit("tool_descriptor_bytes") {
		return NormalizedTool{}, ErrDescriptorInvalid
	}
	var object map[string]json.RawMessage
	if strictjson.Decode(tool.Descriptor, &object, strictjson.Options{MaxBytes: fixedLimit("tool_descriptor_bytes"), MaxDepth: 64}) != nil || object == nil {
		return NormalizedTool{}, ErrDescriptorInvalid
	}
	name, err := requiredString(object, "name", fixedLimit("tool_name_bytes"), false)
	if err != nil || name != tool.UpstreamName || !validToolName(name) {
		return NormalizedTool{}, ErrDescriptorInvalid
	}
	title, err := optionalString(object, "title", fixedLimit("tool_title_bytes"))
	if err != nil {
		return NormalizedTool{}, ErrDescriptorInvalid
	}
	description, err := optionalString(object, "description", fixedLimit("tool_description_bytes"))
	if err != nil {
		return NormalizedTool{}, ErrDescriptorInvalid
	}
	inputRaw, exists := object["inputSchema"]
	if !exists {
		return NormalizedTool{}, ErrDescriptorInvalid
	}
	outputRaw := object["outputSchema"]
	if int64(len(inputRaw)+len(outputRaw)) > fixedLimit("tool_schema_bytes") {
		return NormalizedTool{}, ErrDescriptorInvalid
	}
	input, inputBindings, err := normalizeSchema(inputRaw, options.AllowHeaderBindings)
	if err != nil {
		return NormalizedTool{}, err
	}
	var output json.RawMessage
	if len(outputRaw) != 0 {
		output, _, err = normalizeSchema(outputRaw, false)
		if err != nil {
			return NormalizedTool{}, err
		}
	}
	annotations, err := normalizeAnnotations(object["annotations"])
	if err != nil {
		return NormalizedTool{}, err
	}
	descriptor := contract.NormalizedToolDescriptor{Name: name, Title: title, Description: description, InputSchema: input, OutputSchema: output, Annotations: annotations}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return NormalizedTool{}, ErrDescriptorInvalid
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil || int64(len(canonical)) > fixedLimit("tool_descriptor_bytes") {
		return NormalizedTool{}, ErrDescriptorInvalid
	}
	digest := sha256.Sum256(canonical)
	return NormalizedTool{Key: CandidateKey{ServerID: options.ServerID, UpstreamName: name}, ExternalName: tool.ExternalName, Descriptor: descriptor, Canonical: canonical, Fingerprint: hex.EncodeToString(digest[:]), HeaderBindings: inputBindings}, nil
}

func normalizeSchema(raw json.RawMessage, allowHeaders bool) (json.RawMessage, []HeaderBinding, error) {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return nil, nil, ErrDescriptorInvalid
	}
	var value any
	if err := decodeNumbered(raw, &value); err != nil {
		return nil, nil, ErrDescriptorInvalid
	}
	object, ok := value.(map[string]any)
	if !ok || object["type"] != "object" {
		return nil, nil, ErrDescriptorInvalid
	}
	if dialect, exists := object["$schema"]; exists && dialect != schema2020 {
		return nil, nil, ErrDescriptorInvalid
	}
	if err := validateReferences(value); err != nil {
		return nil, nil, err
	}
	bindings := make([]HeaderBinding, 0)
	headers := make(map[string]struct{})
	if err := collectHeaderBindings(value, nil, false, allowHeaders, headers, &bindings); err != nil {
		return nil, nil, err
	}
	if int64(len(bindings)) > fixedLimit("request_header_count") {
		return nil, nil, ErrDescriptorInvalid
	}
	headerBytes := int64(0)
	for _, binding := range bindings {
		headerBytes += int64(len("Mcp-Param-") + len(binding.Header))
	}
	if headerBytes > fixedLimit("request_header_bytes") {
		return nil, nil, ErrDescriptorInvalid
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(rejectingLoader{})
	const location = "urn:mcp-gateway:schema"
	if err := compiler.AddResource(location, value); err != nil {
		return nil, nil, ErrDescriptorInvalid
	}
	if _, err := compiler.Compile(location); err != nil {
		return nil, nil, ErrDescriptorInvalid
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, nil, ErrDescriptorInvalid
	}
	sort.Slice(bindings, func(left, right int) bool {
		leftPath, rightPath := strings.Join(bindings[left].Path, "\x00"), strings.Join(bindings[right].Path, "\x00")
		if leftPath == rightPath {
			return bindings[left].Header < bindings[right].Header
		}
		return leftPath < rightPath
	})
	return canonical, bindings, nil
}

type rejectingLoader struct{}

func (rejectingLoader) Load(string) (any, error) { return nil, ErrDescriptorInvalid }

func decodeNumbered(raw []byte, destination any) error {
	if err := strictjson.Decode(raw, destination, strictjson.Options{MaxBytes: fixedLimit("tool_schema_bytes"), MaxDepth: 64}); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(destination)
}

func validateReferences(value any) error {
	switch current := value.(type) {
	case map[string]any:
		for key, member := range current {
			switch key {
			case "$ref":
				reference, ok := member.(string)
				if !ok || !strings.HasPrefix(reference, "#") {
					return ErrDescriptorInvalid
				}
			case "$dynamicRef", "$recursiveRef":
				return ErrDescriptorInvalid
			}
			if err := validateReferences(member); err != nil {
				return err
			}
		}
	case []any:
		for _, member := range current {
			if err := validateReferences(member); err != nil {
				return err
			}
		}
	}
	return nil
}

func collectHeaderBindings(value any, path []string, property, allow bool, seen map[string]struct{}, bindings *[]HeaderBinding) error {
	switch current := value.(type) {
	case map[string]any:
		if headerValue, exists := current["x-mcp-header"]; exists {
			header, ok := headerValue.(string)
			kind, kindOK := headerKind(current["type"])
			normalized := strings.ToLower(header)
			if !allow || !property || !ok || !validHeaderName(header) || !kindOK {
				return ErrDescriptorInvalid
			}
			if _, duplicate := seen[normalized]; duplicate {
				return ErrDescriptorInvalid
			}
			seen[normalized] = struct{}{}
			*bindings = append(*bindings, HeaderBinding{Path: append([]string(nil), path...), Header: header, Kind: kind})
		}
		for key, member := range current {
			if key == "x-mcp-header" {
				continue
			}
			if key == "properties" {
				properties, ok := member.(map[string]any)
				if !ok {
					continue
				}
				for name, schema := range properties {
					if err := collectHeaderBindings(schema, append(path, name), true, allow, seen, bindings); err != nil {
						return err
					}
				}
				continue
			}
			if err := collectHeaderBindings(member, path, false, allow, seen, bindings); err != nil {
				return err
			}
		}
	case []any:
		for _, member := range current {
			if err := collectHeaderBindings(member, path, false, allow, seen, bindings); err != nil {
				return err
			}
		}
	}
	return nil
}

func headerKind(value any) (HeaderValueKind, bool) {
	typeName, ok := value.(string)
	if !ok {
		return "", false
	}
	switch typeName {
	case "string":
		return HeaderString, true
	case "boolean":
		return HeaderBoolean, true
	case "integer":
		return HeaderInteger, true
	default:
		return "", false
	}
}

func MirrorHeaders(bindings []HeaderBinding, arguments map[string]any) (map[string]string, error) {
	result := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		value, present := nestedArgument(arguments, binding.Path)
		if !present || value == nil {
			continue
		}
		encoded, err := encodeHeaderValue(binding.Kind, value)
		if err != nil || int64(len(encoded)) > fixedLimit("request_header_value_bytes") {
			return nil, ErrDescriptorInvalid
		}
		result[binding.Header] = encoded
	}
	return result, nil
}

func nestedArgument(arguments map[string]any, path []string) (any, bool) {
	var current any = arguments
	for _, member := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[member]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func encodeHeaderValue(kind HeaderValueKind, value any) (string, error) {
	var text string
	switch kind {
	case HeaderString:
		var ok bool
		text, ok = value.(string)
		if !ok || !utf8.ValidString(text) {
			return "", ErrDescriptorInvalid
		}
	case HeaderBoolean:
		boolean, ok := value.(bool)
		if !ok {
			return "", ErrDescriptorInvalid
		}
		text = strconv.FormatBool(boolean)
	case HeaderInteger:
		integer, ok := safeInteger(value)
		if !ok {
			return "", ErrDescriptorInvalid
		}
		text = strconv.FormatInt(integer, 10)
	default:
		return "", ErrDescriptorInvalid
	}
	if requiresHeaderBase64(text) {
		text = "=?base64?" + base64.StdEncoding.EncodeToString([]byte(text)) + "?="
	}
	return text, nil
}

func safeInteger(value any) (int64, bool) {
	const maximum = int64(1<<53 - 1)
	var integer int64
	switch number := value.(type) {
	case int:
		integer = int64(number)
	case int64:
		integer = number
	case json.Number:
		parsed, err := number.Int64()
		if err != nil {
			return 0, false
		}
		integer = parsed
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < -float64(maximum) || number > float64(maximum) {
			return 0, false
		}
		integer = int64(number)
	default:
		return 0, false
	}
	return integer, integer >= -maximum && integer <= maximum
}

func requiresHeaderBase64(value string) bool {
	if value == "" {
		return false
	}
	if value[0] == ' ' || value[0] == '\t' || value[len(value)-1] == ' ' || value[len(value)-1] == '\t' {
		return true
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return true
		}
	}
	return strings.HasPrefix(value, "=?base64?") && strings.HasSuffix(value, "?=")
}

func validHeaderName(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func normalizeAnnotations(raw json.RawMessage) (contract.NormalizedToolAnnotations, error) {
	result := contract.NormalizedToolAnnotations{DestructiveHint: true, OpenWorldHint: true}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return result, nil
	}
	var object map[string]json.RawMessage
	if strictjson.Decode(raw, &object, strictjson.Options{MaxBytes: fixedLimit("tool_descriptor_bytes"), MaxDepth: 8}) != nil || object == nil {
		return result, ErrDescriptorInvalid
	}
	var err error
	if titleRaw, exists := object["title"]; exists && !bytes.Equal(bytes.TrimSpace(titleRaw), []byte("null")) {
		result.Title, err = optionalString(object, "title", fixedLimit("tool_title_bytes"))
		if err != nil {
			return result, err
		}
	}
	for key, target := range map[string]*bool{"readOnlyHint": &result.ReadOnlyHint, "destructiveHint": &result.DestructiveHint, "idempotentHint": &result.IdempotentHint, "openWorldHint": &result.OpenWorldHint} {
		if rawValue, exists := object[key]; exists && json.Unmarshal(rawValue, target) != nil {
			return result, ErrDescriptorInvalid
		}
	}
	return result, nil
}

func requiredString(object map[string]json.RawMessage, key string, maximum int64, empty bool) (string, error) {
	raw, exists := object[key]
	if !exists {
		return "", ErrDescriptorInvalid
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !utf8.ValidString(value) || int64(len(value)) > maximum || !empty && value == "" {
		return "", ErrDescriptorInvalid
	}
	return value, nil
}

func optionalString(object map[string]json.RawMessage, key string, maximum int64) (*string, error) {
	raw, exists := object[key]
	if !exists {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, ErrDescriptorInvalid
	}
	value, err := requiredString(object, key, maximum, true)
	if err != nil {
		return nil, err
	}
	return &value, nil
}
