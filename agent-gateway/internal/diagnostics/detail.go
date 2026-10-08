package diagnostics

import (
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// Detail is a local-only snapshot, never an error graph or public representation.
// Build it outside domain locks, while the source still owns masking material.
// The sum of escaped field bounds leaves room for the fixed 4 KiB record.
type Detail struct {
	Component   string
	Operation   string
	Resource    string
	Explanation string
	Native      string
	Effect      string
	Excerpt     string
	Stack       string
}

const detailInputLimit = 64 * 1024

var credentialText = regexp.MustCompile(`(?i)(authorization|proxy-authorization|cookie|set-cookie|access_token|refresh_token|client_secret|password|secret|token)([\s"']*[:=][\s"']*)([^\s,;"'}]+)`)
var bearerText = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[^\s,;"']+`)
var privateKeyText = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?(-----END [^-]*PRIVATE KEY-----|$)`)
var urlText = regexp.MustCompile(`https?://[^\s<>"']+`)

// Text masks source-known values before escaping/truncation. It is not a claim
// that arbitrary third-party text is safe to share. Never pass payloads, argv,
// environments, SQL bindings or protocol stdout here.
func Text(value string, limit int, secrets ...string) string {
	if !boundedMasking(secrets) {
		return boundText("detail withheld: masking input exceeds bound", limit)
	}
	cut := len(value) > detailInputLimit
	if cut {
		value = value[:detailInputLimit]
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		// Also mask a secret crossing the bounded-input edge.
		if cut {
			for n := min(len(secret)-1, len(value)); n > 0; n-- {
				if strings.HasSuffix(value, secret[:n]) {
					value = value[:len(value)-n] + "[withheld]"
					break
				}
			}
		}
		value = strings.ReplaceAll(value, secret, "[withheld]")
	}
	value = privateKeyText.ReplaceAllString(value, "[private key withheld]")
	value = bearerText.ReplaceAllString(value, "$1 [withheld]")
	value = credentialText.ReplaceAllString(value, "${1}${2}[withheld]")
	value = urlText.ReplaceAllStringFunc(value, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[URL detail withheld]"
		}
		u.User, u.RawQuery, u.Fragment, u.RawFragment = nil, "", "", ""
		return u.String()
	})
	value = strconv.QuoteToASCII(value)
	value = value[1 : len(value)-1]
	// HTML escaping in the JSON encoder otherwise expands these sixfold.
	value = strings.NewReplacer("<", `\x3c`, ">", `\x3e`, "&", `\x26`).Replace(value)
	const marker = "...[truncated]"
	if len(value) > limit || cut {
		value = value[:min(len(value), max(0, limit-len(marker)))] + marker
	}
	return value
}

func boundedMasking(secrets []string) bool {
	if len(secrets) > 128 {
		return false
	}
	total := 0
	for _, secret := range secrets {
		if len(secret) > detailInputLimit-total {
			return false
		}
		total += len(secret)
	}
	return true
}

func (d Detail) bounded() Detail {
	d.Component = boundText(d.Component, 32)
	d.Operation = boundText(d.Operation, 64)
	d.Resource = boundText(d.Resource, 160)
	d.Explanation = boundText(d.Explanation, 512)
	d.Native = boundText(d.Native, 64)
	d.Effect = boundText(d.Effect, 64)
	d.Excerpt = boundText(d.Excerpt, 192)
	d.Stack = boundText(d.Stack, 192)
	return d
}

// Already escaped snapshots must not be escaped again on submission.
func boundText(s string, limit int) string {
	var out strings.Builder
	encoded := 0
	for _, r := range s {
		token := string(r)
		if r < 0x20 || r >= 0x7f || r == '<' || r == '>' || r == '&' {
			token = strconv.QuoteToASCII(token)
			token = token[1 : len(token)-1]
			if r == '<' || r == '>' || r == '&' {
				token = fmt.Sprintf(`\x%02x`, r)
			}
		}
		size := len(token) + strings.Count(token, `\`) + strings.Count(token, `"`)
		if encoded+size > limit-14 {
			out.WriteString("...[truncated]")
			return out.String()
		}
		out.WriteString(token)
		encoded += size
	}
	return out.String()
}

func Snapshot(component, operation, resource string, err error, secrets ...string) Detail {
	d := Detail{Component: Text(component, 32), Operation: Text(operation, 64), Resource: Text(resource, 160, secrets...)}
	if err == nil {
		return d
	}
	if !boundedMasking(secrets) {
		d.Explanation = "detail withheld: masking input exceeds bound"
		return d
	}
	remaining := 16
	if !finiteError(err, &remaining, make(map[error]bool)) {
		d.Explanation = "error cause traversal truncated (cyclic or more than 16 causes)"
		return d
	}
	budget := 16
	if prior, ok := localDetail(err, &budget); ok {
		if _, direct := err.(interface{ OperatorDetail() Detail }); !direct {
			prior.Explanation = Text(errorText(err), 256, secrets...) + "; " + prior.Explanation
		}
		if d.Resource == "" {
			d.Resource = prior.Resource
		}
		d.Explanation, d.Native, d.Effect, d.Excerpt, d.Stack = prior.Explanation, prior.Native, prior.Effect, prior.Excerpt, prior.Stack
		for _, secret := range secrets {
			if secret == "" {
				continue
			}
			escaped := strconv.QuoteToASCII(secret)
			escaped = strings.NewReplacer("<", `\x3c`, ">", `\x3e`, "&", `\x26`).Replace(escaped[1 : len(escaped)-1])
			for _, field := range []*string{&d.Resource, &d.Explanation, &d.Native, &d.Effect, &d.Excerpt, &d.Stack} {
				// An earlier bounded snapshot may contain only a secret prefix.
				// Complete-value replacement cannot establish safety in that case.
				if strings.Contains(*field, "...[truncated]") {
					*field = "detail withheld: truncated before source masking"
					continue
				}
				*field = strings.ReplaceAll(*field, secret, "[withheld]")
				*field = strings.ReplaceAll(*field, escaped, "[withheld]")
			}
		}
		return d.bounded()
	}
	d.Explanation = Text(errorText(err), 640, secrets...)
	return d
}

func localDetail(err error, remaining *int) (Detail, bool) {
	if err == nil || *remaining == 0 {
		return Detail{}, false
	}
	*remaining--
	if local, ok := err.(interface{ OperatorDetail() Detail }); ok {
		d := local.OperatorDetail()
		if d != (Detail{}) {
			return d, true
		}
	}
	//nolint:errorlint // Inspect this node only; errors.As would bypass the traversal bound.
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		var joined Detail
		found := false
		for _, child := range e.Unwrap() {
			if *remaining == 0 {
				break
			}
			if d, ok := localDetail(child, remaining); ok {
				if !found {
					joined = d
				} else {
					joined.Explanation = boundText(joined.Explanation+"; "+d.Explanation, 640)
				}
				found = true
			}
		}
		return joined, found
	case interface{ Unwrap() error }:
		return localDetail(e.Unwrap(), remaining)
	}
	return Detail{}, false
}

func finiteError(err error, remaining *int, path map[error]bool) bool {
	if err == nil {
		return true
	}
	if *remaining == 0 {
		return false
	}
	*remaining--
	if reflect.TypeOf(err).Comparable() {
		if path[err] {
			return false
		}
		path[err] = true
		defer delete(path, err)
	}
	//nolint:errorlint // Inspect this node only to enforce cycle and node limits.
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		children := e.Unwrap()
		if len(children) > *remaining {
			return false
		}
		for _, child := range children {
			if !finiteError(child, remaining, path) {
				return false
			}
		}
	case interface{ Unwrap() error }:
		return finiteError(e.Unwrap(), remaining, path)
	}
	return true
}

func errorText(err error) (text string) {
	defer func() {
		if recover() != nil {
			text = "error formatter panicked; detail withheld"
		}
	}()
	return err.Error()
}

type localError struct {
	public error
	detail Detail
}

func (e *localError) Error() string          { return e.public.Error() }
func (e *localError) Unwrap() error          { return e.public }
func (e *localError) OperatorDetail() Detail { return e.detail }

// WithDetail retains the unchanged public classification and Error string while
// carrying bounded local evidence. It does not add cleanup causes to errors.Is.
func WithDetail(public error, detail Detail) error {
	if public == nil {
		return nil
	}
	return &localError{public: public, detail: detail.bounded()}
}

// Panic retains source frames, not arbitrary panic objects or request graphs.
func Panic(component, operation, resource string, value any, secrets ...string) Detail {
	var err error
	switch v := value.(type) {
	case error:
		err = v
	case string:
		err = fmt.Errorf("%s", v)
	default:
		err = fmt.Errorf("panic value of type %T (detail withheld)", value)
	}
	d := Snapshot(component, operation, resource, err, secrets...)
	var pcs [24]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	var collected []runtime.Frame
	for {
		f, more := frames.Next()
		if f.Function == "runtime.gopanic" {
			// Recovery machinery precedes gopanic; the failure site follows it.
			collected = collected[:0]
		} else if !strings.HasPrefix(f.Function, "runtime.") {
			collected = append(collected, f)
		}
		if !more {
			break
		}
	}
	var stack strings.Builder
	for _, f := range collected {
		function := f.Function[strings.LastIndex(f.Function, "/")+1:]
		fmt.Fprintf(&stack, "%s:%d %s; ", filepath.Base(f.File), f.Line, function)
	}
	d.Stack = Text(stack.String(), 192, secrets...)
	return d
}

// NativeError is for local finite commands, not public API/protocol projection.
func NativeError(operation, resource string, err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	d := Snapshot("command", operation, resource, err, secrets...)
	return fmt.Errorf("%s %s: %s", d.Operation, d.Resource, d.Explanation)
}
