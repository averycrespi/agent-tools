package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHTTPRejectionVocabularyIsClosedAndBounded(t *testing.T) {
	stages := map[string][]string{
		"headers":      {"invalid_headers", "trailers_unsupported", "upgrade_unsupported", "inner_proxy_authorization"},
		"request_form": {"connect_body", "nested_connect", "origin_form_required", "absolute_http_required"},
		"target":       {"invalid_request_target", "invalid_connect_target"},
	}
	for stage, reasons := range stages {
		for _, reason := range reasons {
			r := HTTPRejection{Stage: stage, Reason: reason}
			if !r.Valid() {
				t.Fatalf("missing category %s/%s", stage, reason)
			}
			raw, err := json.Marshal(r)
			if err != nil || len(raw) > 128 {
				t.Fatalf("unbounded category %s/%s", stage, reason)
			}
			for other := range stages {
				if other != stage && (HTTPRejection{Stage: other, Reason: reason}).Valid() {
					t.Fatal("accepted mismatched stage")
				}
			}
		}
	}
	for _, r := range []HTTPRejection{{}, {Stage: "target", Reason: "raw-error"}, {Stage: strings.Repeat("x", 129), Reason: "invalid_headers"}, {Stage: "target", Reason: strings.Repeat("secret", 1000)}} {
		if r.Valid() {
			t.Fatal("accepted unknown category")
		}
	}
}
