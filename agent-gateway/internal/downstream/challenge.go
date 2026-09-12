package downstream

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

type OAuthChallengeKind string

const (
	OAuthChallengeRefresh OAuthChallengeKind = "refresh"
	OAuthChallengeStepUp  OAuthChallengeKind = "step_up"
)

type OAuthChallengeStage string

const (
	OAuthChallengeModernDiscovery  OAuthChallengeStage = "modern_discovery"
	OAuthChallengeLegacyInitialize OAuthChallengeStage = "legacy_initialize"
	OAuthChallengeCatalogFirstPage OAuthChallengeStage = "catalog_first_page"
)

type OAuthChallengeDisposition struct {
	Kind     OAuthChallengeKind
	Stage    OAuthChallengeStage
	Metadata []string
	Scopes   []string
}

func (*OAuthChallengeDisposition) Error() string {
	return "downstream OAuth challenge requires reconciliation"
}

func (disposition *OAuthChallengeDisposition) at(stage OAuthChallengeStage) *OAuthChallengeDisposition {
	if disposition == nil {
		return nil
	}
	return &OAuthChallengeDisposition{Kind: disposition.Kind, Stage: stage, Metadata: append([]string(nil), disposition.Metadata...), Scopes: append([]string(nil), disposition.Scopes...)}
}

func projectOAuthChallenge(status int, values []string) *OAuthChallengeDisposition {
	if len(values) != 1 || !utf8.ValidString(values[0]) || int64(len(values[0])) > limit("request_header_value_bytes") {
		return nil
	}
	value := strings.TrimSpace(values[0])
	separator := strings.IndexAny(value, " \t")
	if separator <= 0 || !strings.EqualFold(value[:separator], "Bearer") {
		return nil
	}
	parameters := strings.TrimSpace(value[separator:])
	if parameters == "" {
		return nil
	}
	parsed, ok := challengeParameters(parameters)
	if !ok {
		return nil
	}
	kind := OAuthChallengeKind("")
	switch {
	case status == 401 && parsed["error"] == "invalid_token":
		kind = OAuthChallengeRefresh
	case status == 403 && parsed["error"] == "insufficient_scope":
		kind = OAuthChallengeStepUp
	default:
		return nil
	}
	disposition := &OAuthChallengeDisposition{Kind: kind}
	if metadata, present := parsed["resource_metadata"]; present {
		if metadata == "" || !utf8.ValidString(metadata) || int64(len(metadata)) > limit("oauth_url_bytes") || strings.ContainsAny(metadata, "\r\n\x00") {
			return nil
		}
		disposition.Metadata = []string{metadata}
	}
	if kind == OAuthChallengeStepUp {
		scopes, valid := challengeScopes(parsed["scope"])
		if !valid {
			return nil
		}
		disposition.Scopes = scopes
	}
	return disposition
}

func challengeParameters(value string) (map[string]string, bool) {
	parts := make([]string, 0, 4)
	start := 0
	quoted := false
	escaped := false
	for index, character := range value {
		switch {
		case escaped:
			escaped = false
		case quoted && character == '\\':
			escaped = true
		case character == '"':
			quoted = !quoted
		case character == ',' && !quoted:
			parts = append(parts, value[start:index])
			start = index + 1
		}
	}
	if quoted || escaped {
		return nil, false
	}
	parts = append(parts, value[start:])
	parameters := make(map[string]string, len(parts))
	for _, part := range parts {
		key, raw, found := strings.Cut(strings.TrimSpace(part), "=")
		key = strings.ToLower(strings.TrimSpace(key))
		raw = strings.TrimSpace(raw)
		if !found || key == "" || !headerToken.MatchString(key) {
			return nil, false
		}
		if _, duplicate := parameters[key]; duplicate {
			return nil, false
		}
		parsed := raw
		if strings.HasPrefix(raw, `"`) {
			var err error
			parsed, err = strconv.Unquote(raw)
			if err != nil {
				return nil, false
			}
		} else if !headerToken.MatchString(raw) {
			return nil, false
		}
		parameters[key] = parsed
	}
	return parameters, true
}

func challengeScopes(value string) ([]string, bool) {
	if value == "" {
		return nil, true
	}
	if int64(len(value)) > limit("oauth_scope_bytes") || strings.ContainsAny(value, "\t\r\n") {
		return nil, false
	}
	values := strings.Split(value, " ")
	if len(values) > int(limit("oauth_scope_count")) {
		return nil, false
	}
	for _, scope := range values {
		if scope == "" || int64(len(scope)) > limit("oauth_scope_token_bytes") {
			return nil, false
		}
		for _, character := range []byte(scope) {
			if character != 0x21 && (character < 0x23 || character > 0x5b) && (character < 0x5d || character > 0x7e) {
				return nil, false
			}
		}
	}
	return values, true
}
