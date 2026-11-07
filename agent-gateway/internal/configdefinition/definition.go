// Package configdefinition owns bounded v2 creation-input validation and semantic
// comparison. It has no storage, material, runtime, or mutation capability.
package configdefinition

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

const MaxKeyBindings = contract.ConfigV2KeyBindings

// ErrInvalid never includes authored strings or parser errors: those can contain
// accidental secrets even when the schema permits only nonsecret configuration.
var ErrInvalid = errors.New("invalid or unsupported version-2 configuration")

var limits = map[string]int{"agent": 128, "mcp_server": 1024, "mcp_grant": 4096, "http_credential": 256, "http_grant": contract.HTTPPolicyGrants, "git_credential": contract.GitCredentials, "git_repository": contract.GitRepositoryIdentities, "git_grant": contract.GitGrants, "git_routing": 1}

func reference(r contract.ConfigReference, kind string) bool {
	if r.Kind != kind {
		return false
	}
	if kind == "gateway_mcp" {
		return r.Key == ""
	}
	if kind == "mcp_server" {
		return validServerKey(r.Key)
	}
	return contract.ValidResourceKey(r.Key)
}
func validServerKey(key string) bool {
	return len(key) <= 32 && contract.ValidResourceKey(key) && key != contract.SyntheticServerNamespace
}
func identity(kind, key string) string { return kind + "/" + key }

func DecodeDefinition(raw []byte) (contract.ConfigDefinition, error) {
	var d contract.ConfigDefinition
	if decode(raw, &d, contract.ConfigExportMaxBytes) != nil {
		return contract.ConfigDefinition{}, ErrInvalid
	}
	if _, err := Normalize(d); err != nil {
		return contract.ConfigDefinition{}, ErrInvalid
	}
	return d, nil
}

func DecodeBundle(raw []byte) (contract.ConfigBundle, error) {
	var b contract.ConfigBundle
	if decode(raw, &b, contract.ConfigExportMaxBytes) != nil || ValidateBundle(b) != nil {
		return contract.ConfigBundle{}, ErrInvalid
	}
	return b, nil
}

// Normalize returns a validated definition with domain defaults normalized and
// object ordering ignored. Number tokens and array order are preserved exactly.
// It does not decide whether creation is permitted or expose skip/update modes.
func Normalize(d contract.ConfigDefinition) ([]byte, error) {
	if d.Format != contract.ConfigExportFormat || d.Version != contract.ConfigV2Version {
		return nil, ErrInvalid
	}
	if _, ok := limits[d.Kind]; !ok {
		return nil, ErrInvalid
	}
	switch d.Kind {
	case "git_routing":
		if d.Key != "" || d.Reserved {
			return nil, ErrInvalid
		}
	case "mcp_server":
		if !validServerKey(d.Key) {
			return nil, ErrInvalid
		}
	default:
		if !contract.ValidResourceKey(d.Key) {
			return nil, ErrInvalid
		}
	}
	if d.Reserved {
		if !bytes.Equal(bytes.TrimSpace(d.Spec), []byte("null")) {
			return nil, ErrInvalid
		}
	} else {
		normalized, err := normalizeSpec(d)
		if err != nil {
			return nil, ErrInvalid
		}
		d.Spec = normalized
	}
	raw, err := json.Marshal(d)
	if err != nil || len(raw) > contract.ConfigExportMaxBytes {
		return nil, ErrInvalid
	}
	value, err := strictjson.ParseValue(raw, strictjson.Options{MaxBytes: contract.ConfigExportMaxBytes, MaxDepth: 64})
	if err != nil {
		return nil, ErrInvalid
	}
	return canonical(value), nil
}

func Equivalent(a, b contract.ConfigDefinition) (bool, error) {
	left, err := Normalize(a)
	if err != nil {
		return false, err
	}
	right, err := Normalize(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(left, right), nil
}

func normalizeSpec(d contract.ConfigDefinition) ([]byte, error) {
	var spec any
	switch d.Kind {
	case "agent":
		spec = &contract.ConfigAgentSpec{}
	case "mcp_server":
		spec = &contract.ConfigServerSpec{}
	case "mcp_grant":
		spec = &contract.ConfigMCPGrantSpec{}
	case "http_credential":
		spec = &contract.ConfigHTTPCredentialSpec{}
	case "http_grant":
		spec = &contract.ConfigHTTPGrantSpec{}
	case "git_credential":
		spec = &contract.ConfigGitCredentialSpec{}
	case "git_repository":
		spec = &contract.ConfigGitRepositorySpec{}
	case "git_grant":
		spec = &contract.ConfigGitGrantSpec{}
	case "git_routing":
		spec = &contract.ConfigGitRoutingSpec{}
	default:
		return nil, ErrInvalid
	}
	maxBytes := int64(contract.ConfigExportMaxBytes)
	if d.Kind == "mcp_server" {
		maxBytes = 2 * 1024 * 1024
	}
	if d.Kind == "mcp_grant" || d.Kind == "http_grant" || d.Kind == "git_grant" {
		maxBytes = 2 * 1024 * 1024
	}
	if decode(d.Spec, spec, maxBytes) != nil {
		return nil, ErrInvalid
	}
	switch s := spec.(type) {
	case *contract.ConfigAgentSpec:
		if authorization.ValidateConfigurationAgent(*s) != nil {
			return nil, ErrInvalid
		}
	case *contract.ConfigServerSpec:
		old := contract.ConfigServer{Ref: "mcp/" + d.Key, Namespace: d.Key, DisplayName: s.DisplayName, Enabled: s.Enabled, Deleted: s.Deleted, Transport: s.Transport, Credentials: s.Credentials}
		if s.Credentials.Static == nil {
			return nil, ErrInvalid
		}
		if s.Deleted {
			if s.Enabled || !bytes.Equal(bytes.TrimSpace(s.Transport), []byte("null")) || len(s.Credentials.Static) != 0 || s.Credentials.OAuthClientSecret != nil || s.Credentials.OAuthAuthorization {
				return nil, ErrInvalid
			}
		} else {
			transport, err := servers.DecodeTransport(s.Transport)
			if err != nil || !required(s.Transport, reflect.TypeOf(transport)) {
				return nil, ErrInvalid
			}
			normalized, err := json.Marshal(transport)
			if err != nil {
				return nil, ErrInvalid
			}
			s.Transport = normalized
			if !contract.ValidConfigServerCredentials(old) {
				return nil, ErrInvalid
			}
		}
		if servers.ValidateImportedConfiguration([]contract.ConfigServer{old}) != nil {
			return nil, ErrInvalid
		}
	case *contract.ConfigMCPGrantSpec:
		if !reference(s.Agent, "agent") || !reference(s.Server, "mcp_server") && !reference(s.Server, "gateway_mcp") || authorization.ValidateConfigurationMCPGrant(*s) != nil {
			return nil, ErrInvalid
		}
	case *contract.ConfigHTTPCredentialSpec:
		if !validSecretRequirement(s.SecretRef, d, s.Deleted) {
			return nil, ErrInvalid
		}
		def, err := httpcredentials.Normalize(httpcredentials.Definition{Name: s.Name, Boundary: httpcredentials.Boundary{Host: s.Boundary.Host, Port: s.Boundary.Port, AllowWildcard: s.Boundary.AllowWildcard}, Recipe: s.Recipe})
		if err != nil {
			return nil, ErrInvalid
		}
		s.Name = def.Name
		s.Boundary = contract.HTTPCredentialBoundary{Host: def.Boundary.Host, Port: def.Boundary.Port, AllowWildcard: def.Boundary.AllowWildcard}
		s.Recipe = def.Recipe
	case *contract.ConfigHTTPGrantSpec:
		if !reference(s.Agent, "agent") || s.Policy.Credential != nil && !reference(*s.Policy.Credential, "http_credential") || authorization.ValidateConfigurationHTTPGrant(*s) != nil {
			return nil, ErrInvalid
		}
	case *contract.ConfigGitCredentialSpec:
		if !validSecretRequirement(s.SecretRef, d, s.Deleted) {
			return nil, ErrInvalid
		}
		def, err := gitcredentials.Normalize(s.GitCredentialDefinition)
		if err != nil {
			return nil, ErrInvalid
		}
		s.GitCredentialDefinition = def
	case *contract.ConfigGitRepositorySpec:
		if !referenceOptional(s.Credential, "git_credential") || s.Aliases == nil || authorization.ValidateConfigurationGitRepository(*s) != nil {
			return nil, ErrInvalid
		}
	case *contract.ConfigGitGrantSpec:
		if !reference(s.Agent, "agent") || !reference(s.Repository, "git_repository") || authorization.ValidateConfigurationGitGrant(*s) != nil {
			return nil, ErrInvalid
		}
	case *contract.ConfigGitRoutingSpec:
		if s.Origins == nil || authorization.ValidateConfigurationGitRouting(*s) != nil {
			return nil, ErrInvalid
		}
	}
	return json.Marshal(spec)
}
func validSecretRequirement(ref *string, d contract.ConfigDefinition, deleted bool) bool {
	if deleted {
		return ref == nil
	}
	return ref != nil && *ref == identity(d.Kind, d.Key)+"/secret"
}
func referenceOptional(r *contract.ConfigReference, kind string) bool {
	return r == nil || reference(*r, kind)
}

func canonical(v strictjson.Value) []byte {
	switch v.Type {
	case strictjson.ValueObject:
		sort.Slice(v.Object, func(i, j int) bool { return v.Object[i].Name < v.Object[j].Name })
		out := []byte{'{'}
		for i, m := range v.Object {
			if i > 0 {
				out = append(out, ',')
			}
			name, _ := json.Marshal(m.Name)
			out = append(out, name...)
			out = append(out, ':')
			out = append(out, canonical(m.Value)...)
		}
		return append(out, '}')
	case strictjson.ValueArray:
		out := []byte{'['}
		for i, x := range v.Array {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(out, canonical(x)...)
		}
		return append(out, ']')
	case strictjson.ValueNumber:
		return []byte(v.Number)
	case strictjson.ValueString:
		out, _ := json.Marshal(v.String)
		return out
	case strictjson.ValueBoolean:
		if v.Boolean {
			return []byte("true")
		}
		return []byte("false")
	default:
		return []byte("null")
	}
}

func decode(raw []byte, destination any, maxBytes int64) error {
	if strictjson.Decode(raw, destination, strictjson.Options{MaxBytes: maxBytes, MaxDepth: 64, RejectUnknownMembers: true}) != nil || !required(raw, reflect.TypeOf(destination).Elem()) {
		return ErrInvalid
	}
	return nil
}

// Required members and nullability supplement the strict closed token parser.
// Transport unions use the same explicitly selected concrete domain types.
func required(raw json.RawMessage, typ reflect.Type) bool {
	if typ == reflect.TypeFor[json.RawMessage]() {
		return len(raw) > 0
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return typ.Kind() == reflect.Pointer
	}
	if typ.Kind() == reflect.Pointer {
		return required(raw, typ.Elem())
	}
	switch typ.Kind() {
	case reflect.Struct:
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return false
		}
		fields := shapeFields(typ)
		for name := range values {
			if _, ok := fields[name]; !ok {
				return false
			}
		}
		for name, field := range fields {
			member, ok := values[name]
			optional := strings.Contains(field.Tag.Get("json"), ",omitempty")
			if !ok {
				if optional {
					continue
				}
				return false
			}
			if optional && bytes.Equal(bytes.TrimSpace(member), []byte("null")) {
				return false
			}
			if !required(member, field.Type) {
				return false
			}
		}
	case reflect.Interface:
		var mode struct {
			Mode string `json:"mode"`
		}
		if json.Unmarshal(raw, &mode) != nil {
			return false
		}
		if typ == reflect.TypeFor[contract.HTTPAuthentication]() {
			switch mode.Mode {
			case "none":
				return required(raw, reflect.TypeFor[contract.NoAuthentication]())
			case "bearer":
				return required(raw, reflect.TypeFor[contract.BearerAuthentication]())
			case "oauth":
				return required(raw, reflect.TypeFor[contract.OAuthAuthentication]())
			}
		}
		if typ == reflect.TypeFor[contract.OAuthRegistration]() {
			switch mode.Mode {
			case "static":
				return required(raw, reflect.TypeFor[contract.StaticOAuthRegistration]())
			case "dynamic":
				return required(raw, reflect.TypeFor[contract.DynamicOAuthRegistration]())
			}
		}
		return false
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return false
		}
		for _, value := range values {
			if !required(value, typ.Elem()) {
				return false
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return false
		}
		for _, value := range values {
			if !required(value, typ.Elem()) {
				return false
			}
		}
	}
	return true
}
func shapeFields(typ reflect.Type) map[string]reflect.StructField {
	fields := map[string]reflect.StructField{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Anonymous {
			for name, f := range shapeFields(field.Type) {
				fields[name] = f
			}
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name != "-" {
			fields[name] = field
		}
	}
	return fields
}
