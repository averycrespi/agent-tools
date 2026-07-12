// Package outcome classifies exactly associated tool-call results.
package outcome

import "regexp"

type Classification string

const (
	ConfirmedError Classification = "confirmed_error"
	InferredError  Classification = "inferred_error"
	Success        Classification = "success"
	Unknown        Classification = "unknown"
)

type Result struct {
	Name    string
	Content string
	IsError *bool
}

var mcpFailurePattern = regexp.MustCompile(`(?i)\b(?:mcp_call failed|mcp error|fetch failed)\b`)

func IsInferredMCPFailure(callName, resultName string, isError *bool, content string) bool {
	return isError == nil && (callName == "mcp_call" || resultName == "mcp_call") && mcpFailurePattern.MatchString(content)
}

type Accumulator struct {
	confirmed, inferred, success bool
}

func (a *Accumulator) Observe(callName string, result Result) {
	a.confirmed = a.confirmed || result.IsError != nil && *result.IsError
	a.inferred = a.inferred || IsInferredMCPFailure(callName, result.Name, result.IsError, result.Content)
	a.success = a.success || result.IsError != nil && !*result.IsError
}

func (a Accumulator) Classification() Classification {
	switch {
	case a.confirmed:
		return ConfirmedError
	case a.inferred:
		return InferredError
	case a.success:
		return Success
	default:
		return Unknown
	}
}

func ClassifyCall(callName string, results []Result) Classification {
	var accumulator Accumulator
	for _, result := range results {
		accumulator.Observe(callName, result)
	}
	return accumulator.Classification()
}
