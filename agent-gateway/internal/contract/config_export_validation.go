package contract

import (
	"encoding/json"
	"maps"
)

// ValidConfigServerCredentials checks authored requirements without material.
func ValidConfigServerCredentials(s ConfigServer) bool {
	var transport struct {
		Kind              TransportKind     `json:"kind"`
		SecretEnvironment map[string]string `json:"secret_environment"`
		Authentication    struct {
			Mode         AuthenticationMode `json:"mode"`
			Registration struct {
				Mode   RegistrationMode        `json:"mode"`
				Method TokenEndpointAuthMethod `json:"token_endpoint_auth_method"`
			} `json:"registration"`
		} `json:"authentication"`
	}
	if json.Unmarshal(s.Transport, &transport) != nil {
		return false
	}
	expected := map[string]string{}
	oauth, clientSecret := false, false
	switch transport.Kind {
	case TransportStdio:
		for _, slot := range transport.SecretEnvironment {
			expected[slot] = s.Ref + "/static/" + slot
		}
	case TransportStreamableHTTP:
		switch transport.Authentication.Mode {
		case AuthenticationNone:
		case AuthenticationBearer:
			expected["bearer"] = s.Ref + "/static/bearer"
		case AuthenticationOAuth:
			oauth = true
			registration := transport.Authentication.Registration
			clientSecret = registration.Mode == RegistrationStatic && registration.Method != TokenEndpointAuthNone
		default:
			return false
		}
	default:
		return false
	}
	return maps.Equal(expected, s.Credentials.Static) && oauth == s.Credentials.OAuthAuthorization && clientSecret == (s.Credentials.OAuthClientSecret != nil) && (s.Credentials.OAuthClientSecret == nil || *s.Credentials.OAuthClientSecret == s.Ref+"/oauth/client_secret")
}
