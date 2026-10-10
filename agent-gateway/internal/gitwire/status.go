package gitwire

import (
	"bytes"
	"strconv"
	"strings"
)

// StatusObserver is request-local and exposes only validated request-bound
// outcomes, never wire bytes or messages.
// Observation failure does not interfere with forwarding the live response.
type StatusObserver struct {
	refs     map[string]bool
	sideband bool
	data     []byte
	invalid  bool
	outcomes map[string]string
}

func (r *Request) ObserveStatus() *StatusObserver {
	o := &StatusObserver{refs: make(map[string]bool), sideband: r.sideband, invalid: r.operation != "push" || !r.reportStatus}
	for _, action := range r.actions {
		o.refs[action.Ref] = true
	}
	return o
}

func (o *StatusObserver) Write(p []byte) (int, error) {
	if !o.invalid {
		if len(p) > ControlBytes-len(o.data) {
			o.invalid = true
			o.data = nil
		} else {
			o.data = append(o.data, p...)
		}
	}
	return len(p), nil
}

// Result is usable only after a complete identity-encoded HTTP 200 transfer.
// Even a complete report is an upstream claim, not verified repository effects.
func (o *StatusObserver) Result() string {
	o.outcomes = nil
	if o.invalid {
		return "unknown"
	}
	data := o.data
	if o.sideband {
		packets, ok := statusPackets(data)
		if !ok {
			return "unknown"
		}
		var inner []byte
		for _, p := range packets {
			if len(p) < 1 {
				return "unknown"
			}
			switch p[0] {
			case 1:
				inner = append(inner, p[1:]...)
			case 2: // Progress is discarded, never interpreted or retained.
			default:
				return "unknown"
			}
		}
		data = inner
	}
	packets, ok := statusPackets(data)
	if !ok || len(packets) != len(o.refs)+1 {
		return "unknown"
	}
	unpack := string(bytes.TrimSuffix(packets[0], []byte{'\n'}))
	if !strings.HasPrefix(unpack, "unpack ") || len(unpack) == len("unpack ") {
		return "unknown"
	}
	seen := make(map[string]bool)
	outcomes := make(map[string]string)
	passed := 0
	for _, p := range packets[1:] {
		line := string(bytes.TrimSuffix(p, []byte{'\n'}))
		kind, rest, found := strings.Cut(line, " ")
		if !found {
			return "unknown"
		}
		ref := rest
		switch kind {
		case "ok":
			passed++
		case "ng":
			var reason string
			ref, reason, found = strings.Cut(rest, " ")
			if !found || reason == "" {
				return "unknown"
			}
		default:
			return "unknown"
		}
		if !o.refs[ref] || seen[ref] {
			return "unknown"
		}
		seen[ref] = true
		outcomes[ref] = kind
	}
	if unpack != "unpack ok" {
		if passed != 0 {
			return "unknown"
		}
		o.outcomes = outcomes
		return "reported_failure"
	}
	o.outcomes = outcomes
	if passed == len(o.refs) {
		return "reported_success"
	}
	if passed == 0 {
		return "reported_failure"
	}
	return "reported_partial"
}

// RefOutcomes returns only the retained request-order prefix after Result has
// validated the entire report. Invalid/incomplete reports supply no per-ref claim.
func (o *StatusObserver) RefOutcomes(refs []string) []string {
	if o.outcomes == nil || len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		result, ok := o.outcomes[ref]
		if !ok {
			return nil
		}
		out = append(out, result)
	}
	return out
}

func statusPackets(data []byte) ([][]byte, bool) {
	var packets [][]byte
	for len(data) >= 4 {
		header := string(data[:4])
		if strings.Trim(header, "0123456789abcdef") != "" {
			return nil, false
		}
		n, err := strconv.ParseUint(header, 16, 16)
		if err != nil {
			return nil, false
		}
		if n == 0 {
			return packets, len(data) == 4
		}
		if n < 5 || n > 65520 || int(n) > len(data) {
			return nil, false
		}
		packets = append(packets, data[4:n])
		data = data[n:]
	}
	return nil, false
}
