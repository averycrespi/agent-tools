package oauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

const tokenGenerationVersion = 1

var ErrTokenRejected = errors.New("OAuth token response is invalid")

type TokenGeneration struct {
	Version              int      `json:"version"`
	ServerID             string   `json:"server_id"`
	Issuer               string   `json:"issuer"`
	RegistrationRevision string   `json:"registration_revision"`
	Resource             string   `json:"resource"`
	AccessToken          string   `json:"access_token"`
	RefreshToken         *string  `json:"refresh_token"`
	Scopes               []string `json:"scopes"`
	ScopeSpecified       bool     `json:"scope_specified"`
	IssuedAt             string   `json:"issued_at"`
	ExpiresAt            *string  `json:"expires_at"`
}

type parsedTokenResponse struct {
	accessToken    string
	refreshToken   *string
	scopes         []string
	scopeSpecified bool
	expiresAt      *time.Time
}

func parseTokenResponse(status int, header http.Header, body []byte, requested []string, now time.Time) (parsedTokenResponse, error) {
	if status != http.StatusOK {
		return parsedTokenResponse{}, protocolPredicate(ErrTokenRejected, "validate token response", "role=token method=POST http_status=%d expected_status=200", status)
	}
	if !jsonContentType(header) {
		return parsedTokenResponse{}, protocolPredicate(ErrTokenRejected, "validate token response", "role=token http_status=200 rule=json_media_type observed=non_json")
	}
	if int64(len(body)) > limit("oauth_response_body_bytes") {
		return parsedTokenResponse{}, protocolPredicate(ErrTokenRejected, "validate token response", "rule=oauth_response_body_bytes observed=%d allowed=%d", len(body), limit("oauth_response_body_bytes"))
	}
	object, err := decodeObject(body)
	if err != nil {
		return parsedTokenResponse{}, protocolPredicate(ErrTokenRejected, "validate token response", "rule=bounded_unique_member_json_object max_depth=%d; payload withheld", limit("oauth_json_depth"))
	}
	accessToken, err := requiredString(object, "access_token")
	if err != nil || accessToken == "" || int64(len(accessToken)) > limit("keyring_secret_bytes") {
		return parsedTokenResponse{}, protocolPredicate(ErrTokenRejected, "validate token response", "field=access_token rule=nonempty_bounded_string valid_string=%t observed_bytes=%d allowed_bytes=%d", err == nil, len(accessToken), limit("keyring_secret_bytes"))
	}
	tokenType, err := requiredString(object, "token_type")
	if err != nil || len(tokenType) > 32 || !strings.EqualFold(tokenType, "Bearer") {
		return parsedTokenResponse{}, protocolPredicate(ErrTokenRejected, "validate token response", "field=token_type rule=case_insensitive_bearer valid_string=%t observed_bytes=%d allowed_bytes=32; value withheld", err == nil, len(tokenType))
	}
	refresh, refreshPresent, err := optionalString(object, "refresh_token")
	if err != nil || refreshPresent && (refresh == "" || !utf8.ValidString(refresh) || int64(len(refresh)) > limit("keyring_secret_bytes")) {
		return parsedTokenResponse{}, protocolPredicate(ErrTokenRejected, "validate token response", "field=refresh_token rule=nonempty_bounded_utf8_string valid_string=%t observed_bytes=%d allowed_bytes=%d", err == nil, len(refresh), limit("keyring_secret_bytes"))
	}
	var refreshToken *string
	if refreshPresent {
		refreshToken = &refresh
	}
	expires, expiresPresent, err := optionalInteger(object, "expires_in")
	if err != nil || expiresPresent && (expires < 0 || expires > math.MaxInt64/int64(time.Second)) {
		return parsedTokenResponse{}, protocolPredicate(ErrTokenRejected, "validate token response", "field=expires_in rule=bounded_nonnegative_integer valid_integer=%t observed=%d minimum=0 maximum=%d", err == nil, expires, math.MaxInt64/int64(time.Second))
	}
	var expiresAt *time.Time
	if expiresPresent {
		value := now.UTC().Add(time.Duration(expires) * time.Second)
		expiresAt = &value
	}
	scopeText, scopePresent, err := optionalString(object, "scope")
	if err != nil {
		return parsedTokenResponse{}, protocolPredicate(ErrTokenRejected, "validate token response", "field=scope rule=string_if_present")
	}
	var scopes []string
	if scopePresent {
		scopes, err = parseScope(scopeText)
		if err != nil {
			return parsedTokenResponse{}, err
		}
		if requested != nil && !scopeSubset(scopes, requested) {
			return parsedTokenResponse{}, protocolPredicate(ErrTokenRejected, "validate token response", "field=scope rule=requested_scope_subset observed_count=%d requested_count=%d; values withheld", len(scopes), len(requested))
		}
	} else if requested != nil {
		scopes = append([]string(nil), requested...)
		scopePresent = true
	}
	return parsedTokenResponse{accessToken: accessToken, refreshToken: refreshToken, scopes: scopes, scopeSpecified: scopePresent, expiresAt: expiresAt}, nil
}

func parseScope(value string) ([]string, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "  ") {
		return nil, protocolPredicate(ErrTokenRejected, "validate token scope", "field=scope rule=nonempty_single_space_separated_tokens")
	}
	values := strings.Split(value, " ")
	for _, token := range values {
		if token == "" || !utf8.ValidString(token) || strings.ContainsAny(token, "\t\r\n") || int64(len(token)) > limit("oauth_scope_token_bytes") {
			return nil, protocolPredicate(ErrTokenRejected, "validate token scope", "field=scope rule=bounded_utf8_token_without_controls observed_bytes=%d allowed_bytes=%d", len(token), limit("oauth_scope_token_bytes"))
		}
	}
	sort.Strings(values)
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	if int64(len(unique)) > limit("oauth_scope_count") || int64(len(strings.Join(unique, " "))) > limit("oauth_scope_bytes") {
		return nil, protocolPredicate(ErrTokenRejected, "validate token scope", "field=scope rule=scope_bounds observed_count=%d allowed_count=%d observed_bytes=%d allowed_bytes=%d", len(unique), limit("oauth_scope_count"), len(strings.Join(unique, " ")), limit("oauth_scope_bytes"))
	}
	return append([]string(nil), unique...), nil
}

func scopeSubset(values, requested []string) bool {
	allowed := make(map[string]struct{}, len(requested))
	for _, value := range requested {
		allowed[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := allowed[value]; !ok {
			return false
		}
	}
	return true
}

func encodeTokenGeneration(bundle flowBundle, token parsedTokenResponse, issuedAt time.Time) ([]byte, error) {
	binding := TokenGeneration{ServerID: bundle.serverID, Issuer: bundle.registration.Issuer, RegistrationRevision: bundle.registration.Revision, Resource: bundle.graph.Resource}
	return encodeBoundTokenGeneration(binding, token, issuedAt)
}

func encodeBoundTokenGeneration(binding TokenGeneration, token parsedTokenResponse, issuedAt time.Time) ([]byte, error) {
	var expiresAt *string
	if token.expiresAt != nil {
		value := token.expiresAt.UTC().Format(time.RFC3339Nano)
		expiresAt = &value
	}
	generation := TokenGeneration{
		Version: tokenGenerationVersion, ServerID: binding.ServerID, Issuer: binding.Issuer,
		RegistrationRevision: binding.RegistrationRevision, Resource: binding.Resource,
		AccessToken: token.accessToken, RefreshToken: token.refreshToken, Scopes: append([]string(nil), token.scopes...), ScopeSpecified: token.scopeSpecified,
		IssuedAt: issuedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: expiresAt,
	}
	if generation.ScopeSpecified && generation.Scopes == nil {
		generation.Scopes = []string{}
	}
	contents, err := json.Marshal(generation) //nolint:gosec // This is the approved complete oauth_tokens keyring-generation sink.
	if err != nil {
		return nil, ErrTokenRejected
	}
	return contents, nil
}

func DecodeTokenGeneration(contents []byte) (TokenGeneration, error) {
	var generation TokenGeneration
	if err := strictjson.Decode(contents, &generation, strictjson.Options{MaxBytes: limit("keyring_secret_bytes"), MaxDepth: int(limit("oauth_json_depth")), RejectUnknownMembers: true}); err != nil {
		return TokenGeneration{}, protocolPredicate(ErrTokenRejected, "decode token generation", "rule=closed_bounded_generation_json max_depth=%d max_bytes=%d; generation withheld", limit("oauth_json_depth"), limit("keyring_secret_bytes"))
	}
	if generation.Version != tokenGenerationVersion || generation.ServerID == "" || generation.Issuer == "" || generation.RegistrationRevision == "" || generation.Resource == "" || generation.AccessToken == "" || generation.IssuedAt == "" {
		return TokenGeneration{}, protocolPredicate(ErrTokenRejected, "decode token generation", "rule=version_and_required_bindings version=%d expected_version=%d server_present=%t issuer_present=%t revision_present=%t resource_present=%t access_present=%t issued_present=%t", generation.Version, tokenGenerationVersion, generation.ServerID != "", generation.Issuer != "", generation.RegistrationRevision != "", generation.Resource != "", generation.AccessToken != "", generation.IssuedAt != "")
	}
	if _, err := time.Parse(time.RFC3339Nano, generation.IssuedAt); err != nil {
		return TokenGeneration{}, protocolPredicate(ErrTokenRejected, "decode token generation", "field=issued_at rule=rfc3339_timestamp; value withheld")
	}
	if generation.ExpiresAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *generation.ExpiresAt); err != nil {
			return TokenGeneration{}, protocolPredicate(ErrTokenRejected, "decode token generation", "field=expires_at rule=rfc3339_timestamp; value withheld")
		}
	}
	if generation.RefreshToken != nil && *generation.RefreshToken == "" {
		return TokenGeneration{}, protocolPredicate(ErrTokenRejected, "decode token generation", "field=refresh_token rule=nonempty_when_present")
	}
	if generation.ScopeSpecified {
		if generation.Scopes == nil {
			return TokenGeneration{}, protocolPredicate(ErrTokenRejected, "decode token generation", "field=scopes rule=array_when_scope_specified")
		}
		parsed, err := contract.NormalizeOAuthScopes(generation.Scopes)
		if err != nil || !equalStrings(parsed, generation.Scopes) {
			return TokenGeneration{}, protocolPredicate(ErrTokenRejected, "decode token generation", "field=scopes rule=canonical_bounded_scope_array; values withheld")
		}
	} else if generation.Scopes != nil {
		return TokenGeneration{}, protocolPredicate(ErrTokenRejected, "decode token generation", "field=scopes rule=null_when_scope_unspecified")
	}
	return generation, nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TokenRefreshEligible(generation TokenGeneration, now time.Time) bool {
	if generation.ExpiresAt == nil {
		return false
	}
	issuedAt, issuedErr := time.Parse(time.RFC3339Nano, generation.IssuedAt)
	expiresAt, expiresErr := time.Parse(time.RFC3339Nano, *generation.ExpiresAt)
	if issuedErr != nil || expiresErr != nil || !expiresAt.After(issuedAt) {
		return false
	}
	lead := expiresAt.Sub(issuedAt) / 10
	if lead < 5*time.Second {
		lead = 5 * time.Second
	}
	if lead > time.Minute {
		lead = time.Minute
	}
	return !now.Before(expiresAt.Add(-lead))
}

func refreshTokenRequest(refreshToken string, registration servers.OAuthRegistrationAuthority, resource string, clientSecret []byte) (http.Header, []byte, error) {
	if refreshToken == "" || resource == "" {
		return nil, nil, ErrTokenRejected
	}
	values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "resource": {resource}}
	header := http.Header{"Accept": []string{contract.MediaTypeJSON}, "Content-Type": []string{"application/x-www-form-urlencoded"}, "User-Agent": []string{""}}
	switch registration.TokenEndpointAuthMethod {
	case contract.TokenEndpointAuthClientSecretBasic:
		if len(clientSecret) == 0 {
			return nil, nil, ErrTokenRejected
		}
		credentials := url.QueryEscape(registration.ClientID) + ":" + url.QueryEscape(string(clientSecret))
		header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
		values.Set("client_id", registration.ClientID)
	case contract.TokenEndpointAuthClientSecretPost:
		if len(clientSecret) == 0 {
			return nil, nil, ErrTokenRejected
		}
		values.Set("client_id", registration.ClientID)
		values.Set("client_secret", string(clientSecret))
	case contract.TokenEndpointAuthNone:
		values.Set("client_id", registration.ClientID)
	default:
		return nil, nil, ErrTokenRejected
	}
	return header, []byte(values.Encode()), nil
}

func tokenRequest(code string, bundle flowBundle, clientSecret []byte) (http.Header, []byte, error) {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {bundle.registration.CallbackURL},
		"code_verifier": {bundle.verifier},
		"resource":      {bundle.graph.Resource},
	}
	header := http.Header{"Accept": []string{contract.MediaTypeJSON}, "Content-Type": []string{"application/x-www-form-urlencoded"}, "User-Agent": []string{""}}
	switch bundle.registration.TokenEndpointAuthMethod {
	case contract.TokenEndpointAuthClientSecretBasic:
		if len(clientSecret) == 0 {
			return nil, nil, ErrTokenRejected
		}
		credentials := url.QueryEscape(bundle.registration.ClientID) + ":" + url.QueryEscape(string(clientSecret))
		header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
		values.Set("client_id", bundle.registration.ClientID)
	case contract.TokenEndpointAuthClientSecretPost:
		if len(clientSecret) == 0 {
			return nil, nil, ErrTokenRejected
		}
		values.Set("client_id", bundle.registration.ClientID)
		values.Set("client_secret", string(clientSecret))
	case contract.TokenEndpointAuthNone:
		values.Set("client_id", bundle.registration.ClientID)
	default:
		return nil, nil, ErrTokenRejected
	}
	return header, []byte(values.Encode()), nil
}
