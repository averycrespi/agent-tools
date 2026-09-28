package contract

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPTerminationClosedBoundedFacts(t *testing.T) {
	for _, stage := range []string{"exchange", "upstream_read", "downstream_write", "downstream_flush", "deadline", "response_headers", "complete"} {
		for _, condition := range []string{"clean", "cancelled", "timeout", "failure"} {
			for _, context := range []string{"", "cancelled", "timeout"} {
				fact := HTTPTermination{Stage: stage, Condition: condition, Context: context}
				source, method := "upstream", "GET"
				if stage == "exchange" {
					source = "gateway"
				}
				if stage == "response_headers" {
					method = "HEAD"
				}
				outcome := "outcome_unknown"
				valid := stage != "complete"
				if condition == "clean" {
					outcome = "succeeded"
					valid = context == "" && (stage == "complete" || stage == "response_headers")
				}
				require.Equal(t, valid, fact.Valid(outcome, method, source), "%+v", fact)
				raw, err := json.Marshal(fact)
				require.NoError(t, err)
				require.LessOrEqual(t, len(raw), 128)
			}
		}
	}
	for _, fact := range []HTTPTermination{{Stage: "client", Condition: "cancelled"}, {Stage: "upstream_read", Condition: "error text"}, {Stage: "upstream_read", Condition: "failure", Context: "upstream"}} {
		require.False(t, fact.Valid("outcome_unknown", "GET", "upstream"))
	}
}
