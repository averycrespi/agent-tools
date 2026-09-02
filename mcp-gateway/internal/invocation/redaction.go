package invocation

import (
	"errors"
	"fmt"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

const (
	redactedMarker   = "[REDACTED]"
	truncatedCapture = `"[TRUNCATED]"`
)

func RedactArguments(arguments strictjson.Value) ([]byte, error) {
	if arguments.Type != strictjson.ValueObject {
		return nil, errors.New("invocation arguments must be an object")
	}
	redacted := redactValue(arguments)
	capture, err := strictjson.EncodeCompact(redacted)
	if err != nil {
		return nil, fmt.Errorf("encode redacted invocation arguments: %w", err)
	}
	limit, ok := contract.FixedLimitByName("invocation_argument_capture_bytes")
	if !ok || limit.Maximum < 1 {
		return nil, errors.New("invocation argument capture limit is unavailable")
	}
	if int64(len(capture)) > limit.Maximum {
		return []byte(truncatedCapture), nil
	}
	return capture, nil
}

func redactValue(value strictjson.Value) strictjson.Value {
	redacted := value
	switch value.Type {
	case strictjson.ValueObject:
		redacted.Object = make([]strictjson.Member, len(value.Object))
		for index, member := range value.Object {
			redacted.Object[index].Name = member.Name
			if isRedactedKey(member.Name) {
				redacted.Object[index].Value = strictjson.Value{Type: strictjson.ValueString, String: redactedMarker}
			} else {
				redacted.Object[index].Value = redactValue(member.Value)
			}
		}
	case strictjson.ValueArray:
		redacted.Array = make([]strictjson.Value, len(value.Array))
		for index, item := range value.Array {
			redacted.Array[index] = redactValue(item)
		}
	}
	return redacted
}

func isRedactedKey(key string) bool {
	normalized := make([]byte, 0, len(key))
	for index := 0; index < len(key); index++ {
		character := key[index]
		if character >= 0x80 {
			return false
		}
		switch {
		case character >= 'A' && character <= 'Z':
			normalized = append(normalized, character+('a'-'A'))
		case character == '-' || character == '_':
		default:
			normalized = append(normalized, character)
		}
	}
	switch string(normalized) {
	case "authorization", "cookie", "password", "passwd", "secret", "token", "accesstoken", "refreshtoken", "apikey", "privatekey", "clientsecret":
		return true
	default:
		return false
	}
}
