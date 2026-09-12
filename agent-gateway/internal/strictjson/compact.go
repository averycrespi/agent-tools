package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

func EncodeCompact(value Value) ([]byte, error) {
	var output bytes.Buffer
	if err := appendCompact(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func appendCompact(output *bytes.Buffer, value Value) error {
	switch value.Type {
	case ValueNull:
		output.WriteString("null")
	case ValueBoolean:
		if value.Boolean {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
	case ValueString:
		if !utf8.ValidString(value.String) {
			return errors.New("JSON string is not valid UTF-8")
		}
		encoded, err := json.Marshal(value.String)
		if err != nil {
			return fmt.Errorf("encode JSON string: %w", err)
		}
		output.Write(encoded)
	case ValueNumber:
		if !validNumberToken(value.Number) {
			return errors.New("JSON number token is invalid")
		}
		output.WriteString(value.Number)
	case ValueObject:
		output.WriteByte('{')
		seen := make(map[string]struct{}, len(value.Object))
		for index, member := range value.Object {
			if !utf8.ValidString(member.Name) {
				return errors.New("JSON object member is not valid UTF-8")
			}
			if _, duplicate := seen[member.Name]; duplicate {
				return errors.New("JSON has duplicate object member")
			}
			seen[member.Name] = struct{}{}
			if index > 0 {
				output.WriteByte(',')
			}
			name, err := json.Marshal(member.Name)
			if err != nil {
				return fmt.Errorf("encode JSON member: %w", err)
			}
			output.Write(name)
			output.WriteByte(':')
			if err := appendCompact(output, member.Value); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	case ValueArray:
		output.WriteByte('[')
		for index, item := range value.Array {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := appendCompact(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	default:
		return fmt.Errorf("unsupported JSON value type %q", value.Type)
	}
	return nil
}

func validNumberToken(value string) bool {
	if value == "" || (value[0] != '-' && (value[0] < '0' || value[0] > '9')) {
		return false
	}
	return json.Valid([]byte(value))
}
