package workflow

import "testing"

func TestValidateInputsCoercesTypedValuesAndDefaults(t *testing.T) {
	t.Parallel()
	def := inputValidationDefinition()

	values, err := def.ValidateInputs(map[string]string{
		"repo":     "avery/repo",
		"count":    "42",
		"enabled":  "true",
		"priority": "high",
	})
	if err != nil {
		t.Fatalf("ValidateInputs() error = %v", err)
	}

	if values["repo"] != "avery/repo" {
		t.Fatalf("repo = %#v, want avery/repo", values["repo"])
	}
	if values["count"] != 42 {
		t.Fatalf("count = %#v, want 42", values["count"])
	}
	if values["enabled"] != true {
		t.Fatalf("enabled = %#v, want true", values["enabled"])
	}
	if values["mode"] != "safe" {
		t.Fatalf("mode = %#v, want default safe", values["mode"])
	}
}

func TestValidateInputsRejectsMissingRequiredInput(t *testing.T) {
	t.Parallel()
	_, err := inputValidationDefinition().ValidateInputs(map[string]string{"count": "1"})
	assertErrorContains(t, err, "missing required input repo")
}

func TestValidateInputsRejectsUnknownInput(t *testing.T) {
	t.Parallel()
	_, err := inputValidationDefinition().ValidateInputs(map[string]string{
		"repo":    "avery/repo",
		"count":   "1",
		"unknown": "value",
	})
	assertErrorContains(t, err, "unknown input unknown")
}

func TestValidateInputsRejectsInvalidInteger(t *testing.T) {
	t.Parallel()
	_, err := inputValidationDefinition().ValidateInputs(map[string]string{
		"repo":  "avery/repo",
		"count": "many",
	})
	assertErrorContains(t, err, "input count must be an integer")
}

func TestValidateInputsRejectsInvalidBoolean(t *testing.T) {
	t.Parallel()
	_, err := inputValidationDefinition().ValidateInputs(map[string]string{
		"repo":    "avery/repo",
		"count":   "1",
		"enabled": "sometimes",
	})
	assertErrorContains(t, err, "input enabled must be a boolean")
}

func TestValidateInputsRejectsValueOutsideEnum(t *testing.T) {
	t.Parallel()
	_, err := inputValidationDefinition().ValidateInputs(map[string]string{
		"repo":     "avery/repo",
		"count":    "1",
		"priority": "urgent",
	})
	assertErrorContains(t, err, "input priority must be one of high, low")
}

func inputValidationDefinition() *Definition {
	return &Definition{Inputs: map[string]InputSchema{
		"repo": {
			Type:     InputString,
			Required: true,
		},
		"count": {
			Type:     InputInteger,
			Required: true,
		},
		"enabled": {
			Type:    InputBoolean,
			Default: false,
		},
		"mode": {
			Type:    InputString,
			Default: "safe",
		},
		"priority": {
			Type: InputString,
			Enum: []string{"high", "low"},
		},
	}}
}
