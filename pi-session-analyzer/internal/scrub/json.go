package scrub

import (
	"encoding/json"
	"strings"
)

var keyNormalizer = strings.NewReplacer("_", "", "-", "")

var credentialKeys = map[string]bool{
	"apikey": true, "accesstoken": true, "authtoken": true, "authorization": true,
	"awssecretaccesskey": true, "clientsecret": true, "githubtoken": true, "refreshtoken": true,
	"password": true, "passwd": true, "secret": true, "token": true,
}

// JSON scrubs string values, removes thinking fields, and redacts values under explicit credential keys.
func JSON(value string) string {
	var decoded any
	if json.Unmarshal([]byte(value), &decoded) != nil {
		return Scrub(value)
	}
	decoded = scrubJSONValue(decoded)
	data, err := json.Marshal(decoded)
	if err != nil {
		return Scrub(value)
	}
	return string(data)
}

func isCredentialKey(key string) bool {
	if credentialKeys[key] {
		return true
	}
	for _, suffix := range []string{"apikey", "accesstoken", "authtoken", "clientsecret", "refreshtoken", "githubtoken", "password"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

func scrubJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := keyNormalizer.Replace(strings.ToLower(key))
			if normalized == "thinking" || normalized == "thinkingsignature" || normalized == "thinking_signature" {
				delete(typed, key)
				continue
			}
			if isCredentialKey(normalized) {
				typed[key] = "[REDACTED:assignment]"
				continue
			}
			typed[key] = scrubJSONValue(child)
		}
		return typed
	case []any:
		for i := range typed {
			typed[i] = scrubJSONValue(typed[i])
		}
		return typed
	case string:
		return Scrub(typed)
	default:
		return value
	}
}
