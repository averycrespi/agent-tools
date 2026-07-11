package scrub

import (
	"encoding/json"
	"strings"
)

var credentialKeys = map[string]bool{
	"apikey": true, "api_key": true, "api-key": true,
	"accesstoken": true, "access_token": true, "access-token": true,
	"authtoken": true, "auth_token": true, "auth-token": true,
	"authorization": true, "aws_secret_access_key": true, "aws-secret-access-key": true,
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

func scrubJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(key)
			if normalized == "thinking" || normalized == "thinkingsignature" || normalized == "thinking_signature" {
				delete(typed, key)
				continue
			}
			if credentialKeys[normalized] {
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
