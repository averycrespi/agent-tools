// Package strictjson provides dependency-neutral bounded parsing for untrusted JSON.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"unicode/utf8"
)

var ErrTooLarge = errors.New("JSON exceeds byte limit")

type Options struct {
	MaxBytes             int64
	MaxDepth             int
	RejectUnknownMembers bool
}

func DecodeReader(reader io.Reader, destination any, options Options) error {
	if options.MaxBytes < 1 {
		return errors.New("JSON byte limit must be positive")
	}
	contents, err := io.ReadAll(io.LimitReader(reader, options.MaxBytes+1))
	if err != nil {
		return fmt.Errorf("read JSON: %w", err)
	}
	if int64(len(contents)) > options.MaxBytes {
		return ErrTooLarge
	}
	return Decode(contents, destination, options)
}

func Decode(contents []byte, destination any, options Options) error {
	if options.MaxBytes < 1 {
		return errors.New("JSON byte limit must be positive")
	}
	if options.MaxDepth < 1 {
		return errors.New("JSON depth limit must be positive")
	}
	if int64(len(contents)) > options.MaxBytes {
		return ErrTooLarge
	}
	if !utf8.Valid(contents) {
		return errors.New("JSON is not valid UTF-8")
	}
	if err := validate(contents, options.MaxDepth); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if options.RejectUnknownMembers {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	return nil
}

func CanonicalEqual(left, right []byte, options Options) (bool, error) {
	leftValue, err := decodeValue(left, options)
	if err != nil {
		return false, fmt.Errorf("decode left JSON: %w", err)
	}
	rightValue, err := decodeValue(right, options)
	if err != nil {
		return false, fmt.Errorf("decode right JSON: %w", err)
	}
	return equalValues(leftValue, rightValue)
}

func decodeValue(contents []byte, options Options) (any, error) {
	var value any
	if err := Decode(contents, &value, options); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func validate(contents []byte, maximumDepth int) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := validateValue(decoder, 0, maximumDepth); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON has trailing data")
	}
	return nil
}

func validateValue(decoder *json.Decoder, depth, maximumDepth int) error {
	if depth > maximumDepth {
		return errors.New("JSON exceeds depth limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read JSON token: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return fmt.Errorf("read JSON member: %w", keyErr)
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object member is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("JSON has duplicate object member")
			}
			seen[key] = struct{}{}
			if err := validateValue(decoder, depth+1, maximumDepth); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := validateValue(decoder, depth+1, maximumDepth); err != nil {
				return err
			}
		}
	default:
		return errors.New("JSON has unexpected delimiter")
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("close JSON value: %w", err)
	}
	return nil
}

func equalValues(left, right any) (bool, error) {
	switch leftValue := left.(type) {
	case nil:
		return right == nil, nil
	case bool:
		rightValue, ok := right.(bool)
		return ok && leftValue == rightValue, nil
	case string:
		rightValue, ok := right.(string)
		return ok && leftValue == rightValue, nil
	case json.Number:
		rightValue, ok := right.(json.Number)
		if !ok {
			return false, nil
		}
		leftNumber, leftOK := new(big.Rat).SetString(string(leftValue))
		rightNumber, rightOK := new(big.Rat).SetString(string(rightValue))
		if !leftOK || !rightOK {
			return false, errors.New("JSON number cannot be compared")
		}
		return leftNumber.Cmp(rightNumber) == 0, nil
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false, nil
		}
		for index := range leftValue {
			equal, err := equalValues(leftValue[index], rightValue[index])
			if err != nil || !equal {
				return equal, err
			}
		}
		return true, nil
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false, nil
		}
		for key, value := range leftValue {
			rightMember, exists := rightValue[key]
			if !exists {
				return false, nil
			}
			equal, err := equalValues(value, rightMember)
			if err != nil || !equal {
				return equal, err
			}
		}
		return true, nil
	default:
		return false, fmt.Errorf("unsupported decoded JSON type %T", left)
	}
}
