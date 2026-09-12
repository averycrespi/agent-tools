package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

type ValueType string

const (
	ValueNull    ValueType = "null"
	ValueBoolean ValueType = "boolean"
	ValueString  ValueType = "string"
	ValueNumber  ValueType = "number"
	ValueObject  ValueType = "object"
	ValueArray   ValueType = "array"
)

type Member struct {
	Name  string
	Value Value
}

type Value struct {
	Type    ValueType
	Boolean bool
	String  string
	Number  string
	Object  []Member
	Array   []Value
}

func ParseValueReader(reader io.Reader, options Options) (Value, error) {
	if options.MaxBytes < 1 {
		return Value{}, errors.New("JSON byte limit must be positive")
	}
	contents, err := io.ReadAll(io.LimitReader(reader, options.MaxBytes+1))
	if err != nil {
		return Value{}, fmt.Errorf("read JSON: %w", err)
	}
	if int64(len(contents)) > options.MaxBytes {
		return Value{}, ErrTooLarge
	}
	return ParseValue(contents, options)
}

func ParseValue(contents []byte, options Options) (Value, error) {
	if options.MaxBytes < 1 {
		return Value{}, errors.New("JSON byte limit must be positive")
	}
	if options.MaxDepth < 1 {
		return Value{}, errors.New("JSON depth limit must be positive")
	}
	if int64(len(contents)) > options.MaxBytes {
		return Value{}, ErrTooLarge
	}
	if !utf8.Valid(contents) {
		return Value{}, errors.New("JSON is not valid UTF-8")
	}
	if err := validate(contents, options.MaxDepth); err != nil {
		return Value{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	value, err := parseValue(decoder, 0, options.MaxDepth)
	if err != nil {
		return Value{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Value{}, errors.New("JSON has trailing data")
	}
	return value, nil
}

func parseValue(decoder *json.Decoder, depth, maximumDepth int) (Value, error) {
	if depth > maximumDepth {
		return Value{}, errors.New("JSON exceeds depth limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return Value{}, fmt.Errorf("read JSON token: %w", err)
	}
	switch token := token.(type) {
	case nil:
		return Value{Type: ValueNull}, nil
	case bool:
		return Value{Type: ValueBoolean, Boolean: token}, nil
	case string:
		return Value{Type: ValueString, String: token}, nil
	case json.Number:
		return Value{Type: ValueNumber, Number: string(token)}, nil
	case json.Delim:
		switch token {
		case '{':
			members := make([]Member, 0)
			for decoder.More() {
				nameToken, nameErr := decoder.Token()
				if nameErr != nil {
					return Value{}, fmt.Errorf("read JSON member: %w", nameErr)
				}
				name, ok := nameToken.(string)
				if !ok {
					return Value{}, errors.New("JSON object member is not a string")
				}
				memberValue, valueErr := parseValue(decoder, depth+1, maximumDepth)
				if valueErr != nil {
					return Value{}, valueErr
				}
				members = append(members, Member{Name: name, Value: memberValue})
			}
			if _, err := decoder.Token(); err != nil {
				return Value{}, fmt.Errorf("close JSON value: %w", err)
			}
			return Value{Type: ValueObject, Object: members}, nil
		case '[':
			items := make([]Value, 0)
			for decoder.More() {
				item, itemErr := parseValue(decoder, depth+1, maximumDepth)
				if itemErr != nil {
					return Value{}, itemErr
				}
				items = append(items, item)
			}
			if _, err := decoder.Token(); err != nil {
				return Value{}, fmt.Errorf("close JSON value: %w", err)
			}
			return Value{Type: ValueArray, Array: items}, nil
		default:
			return Value{}, errors.New("JSON has unexpected delimiter")
		}
	default:
		return Value{}, fmt.Errorf("unsupported decoded JSON token %T", token)
	}
}
