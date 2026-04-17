package rules

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/PaesslerAG/jsonpath"
)

// matchBody evaluates the body matcher stored on r against the buffered body
// and Content-Type in req. It returns (matched bool, bypassError string).
//
// bypassError is non-empty only when the body could not be evaluated due to a
// size or timeout cap; in that case matched is always false and the caller
// should record the bypass reason in the audit log.
//
// If r has no body matcher, matchBody returns (true, "").
func matchBody(r *Rule, req *Request) (matched bool, bypassError string) {
	if r.body == nil {
		// No body constraint — wildcard.
		return true, ""
	}

	// Empty body never matches a body-matcher rule (design §4).
	if len(req.Body) == 0 {
		return false, ""
	}

	// Bypass: body was not fully buffered.
	if req.BodyTruncated {
		return false, "body_matcher_bypassed:size"
	}
	if req.BodyTimedOut {
		return false, "body_matcher_bypassed:timeout"
	}

	ct := req.Header.Get("Content-Type")

	switch bm := r.body.(type) {
	case *JSONBodyMatch:
		return matchJSONBody(bm, ct, req.Body)
	case *FormBodyMatch:
		return matchFormBody(bm, ct, req.Body)
	case *TextBodyMatch:
		return matchTextBody(bm, ct, req.Body)
	default:
		// Unknown body matcher type — treat as no-match (defensive).
		return false, ""
	}
}

// matchJSONBody matches a json_body block. Content-Type must start with
// "application/json". All declared JSONPath matchers must match (AND semantics).
func matchJSONBody(bm *JSONBodyMatch, ct string, body []byte) (bool, string) {
	// Strip parameters from Content-Type for prefix comparison.
	mediaType, _, _ := strings.Cut(ct, ";")
	mediaType = strings.TrimSpace(mediaType)
	if !strings.EqualFold(mediaType, "application/json") {
		return false, ""
	}

	// Unmarshal once.
	var doc interface{}
	if err := json.Unmarshal(body, &doc); err != nil {
		return false, ""
	}

	for i := range bm.Paths {
		pm := &bm.Paths[i]
		result, err := jsonpath.Get(pm.Path, doc)
		if err != nil {
			// Path not found or evaluation error — no match.
			return false, ""
		}
		if !matchJSONValues(pm, result) {
			return false, ""
		}
	}
	return true, ""
}

// matchJSONValues checks whether any value in result matches the compiled regex
// on pm. result may be a scalar or a []interface{} (from a wildcard path).
func matchJSONValues(pm *JSONPathMatcher, result interface{}) bool {
	switch v := result.(type) {
	case []interface{}:
		// Wildcard result — any element matching is sufficient.
		for _, elem := range v {
			if pm.re.MatchString(stringify(elem)) {
				return true
			}
		}
		return false
	default:
		return pm.re.MatchString(stringify(v))
	}
}

// stringify converts a JSON scalar value to its string representation for
// regex matching. Numeric values use %v (no trailing zeros or exponential
// notation for integers).
func stringify(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", val)
	}
}

// matchFormBody matches a form_body block. Content-Type must start with
// "application/x-www-form-urlencoded". All declared field matchers must match.
func matchFormBody(bm *FormBodyMatch, ct string, body []byte) (bool, string) {
	mediaType, _, _ := strings.Cut(ct, ";")
	mediaType = strings.TrimSpace(mediaType)
	if !strings.EqualFold(mediaType, "application/x-www-form-urlencoded") {
		return false, ""
	}

	values, err := url.ParseQuery(string(body))
	if err != nil {
		return false, ""
	}

	for i := range bm.Fields {
		fm := &bm.Fields[i]
		fieldVals, ok := values[fm.Field]
		if !ok || len(fieldVals) == 0 {
			return false, ""
		}
		// Any value for this field matching is sufficient; all fields must have
		// at least one matching value.
		matched := false
		for _, fv := range fieldVals {
			if fm.re.MatchString(fv) {
				matched = true
				break
			}
		}
		if !matched {
			return false, ""
		}
	}
	return true, ""
}

// matchTextBody matches a text_body block. Content-Type must start with "text/".
func matchTextBody(bm *TextBodyMatch, ct string, body []byte) (bool, string) {
	mediaType, _, _ := strings.Cut(ct, ";")
	mediaType = strings.TrimSpace(strings.ToLower(mediaType))
	if !strings.HasPrefix(mediaType, "text/") {
		return false, ""
	}
	return bm.re.Match(body), ""
}
