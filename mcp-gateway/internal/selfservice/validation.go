package selfservice

import (
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/grantrequests"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

func validateEmptyInput(arguments strictjson.Value) error {
	var input struct{}
	return decodeArguments(arguments, &input)
}

func validateListGrantsInput(arguments strictjson.Value) error {
	var input contract.ListGrantsInput
	if err := decodeArguments(arguments, &input); err != nil {
		return err
	}
	return validateOptionalCursor(input.Cursor)
}

func validateCreateRequestInput(arguments strictjson.Value) error {
	var input contract.CreateGrantRequestInput
	if err := decodeArguments(arguments, &input); err != nil {
		return err
	}
	return grantrequests.ValidateRequestPolicy(input.Policy)
}

func validateRequestIDInput(arguments strictjson.Value) error {
	var input contract.GrantRequestIDInput
	return decodeArguments(arguments, &input)
}

func validateListRequestsInput(arguments strictjson.Value) error {
	var input contract.ListGrantRequestsInput
	if err := decodeArguments(arguments, &input); err != nil {
		return err
	}
	if input.State != nil {
		if _, err := contract.ParseGrantRequestState(string(*input.State)); err != nil {
			return err
		}
	}
	return validateOptionalCursor(input.Cursor)
}

func validateOptionalCursor(cursor *string) error {
	if cursor == nil {
		return nil
	}
	return ValidateCursorSyntax(*cursor)
}

func decodeArguments(arguments strictjson.Value, destination any) error {
	encoded, err := strictjson.EncodeCompact(arguments)
	if err != nil {
		return err
	}
	if err := strictjson.Decode(encoded, destination, strictjson.Options{
		MaxBytes: mustSelfServiceLimit("downstream_mcp_body_bytes"), MaxDepth: int(mustSelfServiceLimit("json_depth")), RejectUnknownMembers: true,
	}); err != nil {
		return err
	}
	return nil
}
