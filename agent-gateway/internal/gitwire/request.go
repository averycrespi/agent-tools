// Package gitwire owns request-local Git wire controls, never retained evidence.
package gitwire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

var ErrInvalid = errors.New("unsupported Git request")

const (
	ControlBytes    = 256 * 1024
	CapabilityBytes = 4096
	ParseTimeout    = 10 * time.Second
)

// Route classifies coordinates before HTTP policy. Invalid Git-shaped requests
// remain Git-shaped; neither content type nor an unknown repository permits fallback.
func Route(target httppolicy.Request, locators ...string) (base, operation string, shaped bool, err error) {
	u := target.URL()
	decodedPath := strings.ToLower(u.Path)
	// Inspect escaped spellings only for rejection, never repository selection.
	// Multiple encoding layers cannot turn Git-shaped traffic into ordinary HTTP.
	for range len(decodedPath)/2 + 1 {
		decoded, e := url.PathUnescape(decodedPath)
		if e != nil || decoded == decodedPath {
			break
		}
		decodedPath = strings.ToLower(decoded)
	}
	// Inspect only coordinates relative to a repository root, never arbitrary
	// asset names or query text. Configured roots also support hosted subpaths;
	// unknown GitHub repositories use the owner/repository coordinate pair.
	cleaned := path.Clean(decodedPath)
	root := ""
	for _, locator := range locators {
		candidate, e := url.Parse(locator)
		if e != nil || candidate.Scheme != u.Scheme || candidate.Host != u.Host {
			continue
		}
		candidatePath := strings.ToLower(candidate.Path)
		if strings.HasPrefix(cleaned, candidatePath+"/") && len(candidatePath) > len(root) {
			root = candidatePath
		}
	}
	remaining := ""
	if root != "" {
		remaining = strings.TrimPrefix(cleaned, root+"/")
	} else {
		segments := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
		depth := 2
		if len(segments) > 0 && strings.HasSuffix(segments[0], ".git") {
			depth = 1
		}
		if len(segments) > depth {
			remaining = strings.Join(segments[depth:], "/")
		}
	}
	first, rest, _ := strings.Cut(remaining, "/")
	second, _, _ := strings.Cut(rest, "/")
	shaped = first == "git-upload-pack" || first == "git-receive-pack" || first == "head" || first == "objects" || first == "info" && slices.Contains([]string{"refs", "lfs", "alternates", "http-alternates"}, second)
	query, queryErr := url.ParseQuery(u.RawQuery)
	if !shaped {
		return "", "", false, nil
	}
	if queryErr != nil || strings.Contains(u.EscapedPath(), "%") || u.ForceQuery {
		return "", "", true, ErrInvalid
	}
	suffix := ""
	switch {
	case strings.HasSuffix(u.Path, "/info/refs"):
		suffix = "/info/refs"
		if target.Method() != http.MethodGet || len(query) != 1 || len(query["service"]) != 1 {
			return "", "", true, ErrInvalid
		}
		switch query.Get("service") {
		case "git-upload-pack":
			operation = "read_discovery"
		case "git-receive-pack":
			operation = "push_discovery"
		default:
			return "", "", true, ErrInvalid
		}
		if u.RawQuery != "service="+query.Get("service") {
			return "", "", true, ErrInvalid
		}
	case strings.HasSuffix(u.Path, "/git-upload-pack"):
		suffix, operation = "/git-upload-pack", "read"
	case strings.HasSuffix(u.Path, "/git-receive-pack"):
		suffix, operation = "/git-receive-pack", "push"
	default:
		return "", "", true, ErrInvalid
	}
	if operation == "read" || operation == "push" {
		if target.Method() != http.MethodPost || u.RawQuery != "" {
			return "", "", true, ErrInvalid
		}
	}
	base, err = gitpolicy.Locator(u.Scheme + "://" + u.Host + strings.TrimSuffix(u.Path, suffix))
	return base, operation, true, err
}

// Request keeps the exact bytes and source private. Copies of public summaries
// cannot manufacture or replace its one dispatch body.
type Request struct {
	target          httppolicy.Request
	repository      contract.GitRepository
	profileRevision string
	operation       string
	actions         []gitpolicy.RefAction
	prefix          []byte
	source          io.ReadCloser
	used            atomic.Bool
}

func New(ctx context.Context, target httppolicy.Request, repository contract.GitRepository, profileRevision string, header http.Header, body io.ReadCloser) (*Request, error) {
	base, operation, shaped, err := Route(target, append([]string{repository.URL}, repository.Aliases...)...)
	if err != nil || !shaped || !gitpolicy.ValidRevision(profileRevision) || (base != repository.URL && !slices.Contains(repository.Aliases, base)) {
		return nil, ErrInvalid
	}
	if len(header.Values("Content-Encoding")) != 0 || len(header.Values("Content-Type")) > 1 || len(header.Values("Git-Protocol")) > 1 {
		return nil, ErrInvalid
	}
	protocol := header.Get("Git-Protocol")
	if protocol != "" && protocol != "version=0" && protocol != "version=1" && protocol != "version=2" {
		return nil, ErrInvalid
	}
	contentType := header.Get("Content-Type")
	if operation == "push" && contentType != "application/x-git-receive-pack-request" || operation == "read" && contentType != "application/x-git-upload-pack-request" || strings.HasSuffix(operation, "discovery") && contentType != "" {
		return nil, ErrInvalid
	}
	repository.Aliases = slices.Clone(repository.Aliases)
	if repository.CredentialID != nil {
		id := *repository.CredentialID
		repository.CredentialID = &id
	}
	request := &Request{target: target, repository: repository, profileRevision: profileRevision, operation: operation, source: body}
	if strings.HasSuffix(operation, "discovery") {
		if err := exactEnd(ctx, body); err != nil {
			return nil, err
		}
	} else if operation == "push" {
		if err := request.parse(ctx); err != nil {
			return nil, err
		}
	}
	return request, nil
}
func (r *Request) Target() httppolicy.Request { return r.target }
func (r *Request) Repository() contract.GitRepository {
	out := r.repository
	out.Aliases = slices.Clone(out.Aliases)
	if out.CredentialID != nil {
		id := *out.CredentialID
		out.CredentialID = &id
	}
	return out
}
func (r *Request) ProfileRevision() string        { return r.profileRevision }
func (r *Request) Operation() string              { return r.operation }
func (r *Request) Actions() []gitpolicy.RefAction { return slices.Clone(r.actions) }
func (r *Request) PushCapable() bool {
	return r.operation == "push_discovery" || r.operation == "probe"
}
func (r *Request) Dispatch() (io.ReadCloser, error) {
	if !r.used.CompareAndSwap(false, true) {
		return nil, ErrInvalid
	}
	return &forwardBody{Reader: io.MultiReader(bytes.NewReader(r.prefix), r.source), Closer: r.source}, nil
}

type forwardBody struct {
	io.Reader
	io.Closer
}

func exactEnd(ctx context.Context, reader io.Reader) error {
	if ctx.Err() != nil {
		return ErrInvalid
	}
	var one [1]byte
	n, err := reader.Read(one[:])
	if n != 0 || err != io.EOF || ctx.Err() != nil {
		return ErrInvalid
	}
	return nil
}
func oid(value string) bool { return len(value) == 40 && strings.Trim(value, "0123456789abcdef") == "" }
func (r *Request) parse(ctx context.Context) error {
	deadline := time.Now().Add(ParseTimeout)
	seen := map[string]bool{}
	needsPack := false
	for {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return ErrInvalid
		}
		var framing [4]byte
		if _, err := io.ReadFull(r.source, framing[:]); err != nil {
			return ErrInvalid
		}
		size, err := strconv.ParseUint(string(framing[:]), 16, 16)
		if err != nil || strings.Trim(string(framing[:]), "0123456789abcdef") != "" {
			return ErrInvalid
		}
		if len(r.prefix)+4 > ControlBytes {
			return ErrInvalid
		}
		r.prefix = append(r.prefix, framing[:]...)
		if size == 0 {
			break
		}
		if size < 5 || size > 65520 || len(r.prefix)+int(size)-4 > ControlBytes || len(r.actions) >= contract.GitRequestedRefs {
			return ErrInvalid
		}
		line := make([]byte, int(size)-4)
		if _, err := io.ReadFull(r.source, line); err != nil {
			return ErrInvalid
		}
		r.prefix = append(r.prefix, line...)
		control := string(line)
		if len(r.actions) == 0 {
			command, capabilities, found := strings.Cut(control, "\x00")
			if found {
				if !validCapabilities(capabilities) {
					return ErrInvalid
				}
				control = command
			}
		}
		parts := strings.Split(control, " ")
		if len(parts) != 3 || !oid(parts[0]) || !oid(parts[1]) || !gitpolicy.ValidRef(parts[2]) || !strings.HasPrefix(parts[2], "refs/heads/") && !strings.HasPrefix(parts[2], "refs/tags/") || seen[parts[2]] {
			return ErrInvalid
		}
		seen[parts[2]] = true
		zero := strings.Repeat("0", 40)
		action := "update"
		if parts[0] == zero {
			action = "create"
		}
		if parts[1] == zero {
			action = "delete"
		}
		if parts[0] == zero && parts[1] == zero {
			return ErrInvalid
		}
		needsPack = needsPack || action != "delete"
		r.actions = append(r.actions, gitpolicy.RefAction{Ref: parts[2], Action: action})
	}
	if ctx.Err() != nil || !time.Now().Before(deadline) {
		return ErrInvalid
	}
	if len(r.actions) == 0 {
		r.operation = "probe"
	}
	if !needsPack {
		return exactEnd(ctx, r.source)
	}
	// The opaque pack is never parsed or buffered beyond its fixed signature.
	var signature [4]byte
	if _, err := io.ReadFull(r.source, signature[:]); err != nil || string(signature[:]) != "PACK" || ctx.Err() != nil || !time.Now().Before(deadline) {
		return ErrInvalid
	}
	r.prefix = append(r.prefix, signature[:]...)
	return nil
}
func validCapabilities(value string) bool {
	if len(value) == 0 || len(value) > CapabilityBytes {
		return false
	}
	// Native send-pack prefixes its capability list with one space after NUL.
	// Preserve the prefix byte-for-byte while validating the token view.
	value = strings.TrimPrefix(value, " ")
	seen := map[string]bool{}
	tokens := strings.Split(value, " ")
	if len(tokens) > 32 {
		return false
	}
	for _, token := range tokens {
		key, _, _ := strings.Cut(token, "=")
		if token == "" || seen[key] {
			return false
		}
		seen[key] = true
		switch token {
		case "report-status", "report-status-v2", "delete-refs", "side-band-64k", "quiet", "atomic", "ofs-delta", "object-format=sha1":
		default:
			if !strings.HasPrefix(token, "agent=") || len(token) <= 6 || len(token) > 256 {
				return false
			}
			for _, b := range []byte(token) {
				if b < 0x21 || b > 0x7e {
					return false
				}
			}
		}
	}
	return true
}
