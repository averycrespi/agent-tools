// Package gitpolicy owns bounded pure Git selectors, not authentication or I/O.
package gitpolicy

import (
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

var ErrInvalid = errors.New("invalid Git policy or locator")

func ValidRevision(value string) bool {
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == value
}

// Locator accepts canonical HTTPS coordinates with an intentionally conservative
// ASCII segment grammar. No query, escaping, userinfo or URL equivalence guesses
// can select a different repository. Canonical output retains the base path.
func Locator(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > contract.GitLocatorBytes || strings.ContainsAny(raw, "%?#\\") {
		return "", ErrInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Host == "" || u.Path == "" || u.Path == "/" || strings.HasSuffix(u.Path, "/") {
		return "", ErrInvalid
	}
	request, err := httppolicy.ParseRequest(raw, "GET", u.Host, "", nil)
	if err != nil {
		return "", ErrInvalid
	}
	for _, segment := range strings.Split(u.Path[1:], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrInvalid
		}
		for _, b := range []byte(segment) {
			if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~", rune(b)) {
				return "", ErrInvalid
			}
		}
	}
	return request.URL().String(), nil
}

func Origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", ErrInvalid
	}
	locator, err := Locator(raw + "/repository")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(locator, "/repository"), nil
}

// Only explicit .git spelling aliases are supported. They preserve the exact
// canonical origin/base destination; arbitrary alternate hosts/paths are not
// aliases and require a new repository identity and grants.
func Aliases(canonical string, values []string) ([]string, error) {
	if values == nil || len(values) > contract.GitAliases {
		return nil, ErrInvalid
	}
	base := strings.TrimSuffix(canonical, ".git")
	out := make([]string, 0, len(values))
	for _, raw := range values {
		alias, err := Locator(raw)
		if err != nil || alias == canonical || alias != base && alias != base+".git" || slices.Contains(out, alias) {
			return nil, ErrInvalid
		}
		out = append(out, alias)
	}
	slices.Sort(out)
	return out, nil
}

func Overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func ValidRef(value string) bool {
	if len(value) > contract.GitRefBytes || !strings.HasPrefix(value, "refs/") || strings.Count(value, "/") < 2 || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, b := range []byte(value) {
		if b < 0x21 || b > 0x7e || strings.ContainsRune("~^:?*[\\", rune(b)) {
			return false
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") || strings.HasSuffix(segment, ".lock") {
			return false
		}
	}
	return true
}

func Normalize(policy contract.GitPolicy) (contract.GitPolicy, error) {
	if policy.Version != 1 || policy.Refs == nil || len(policy.Refs) > contract.GitRules || len(policy.Refs) != 0 && !policy.Read {
		return contract.GitPolicy{}, ErrInvalid
	}
	out := contract.GitPolicy{Version: 1, Read: policy.Read, Refs: make([]contract.GitRefRule, 0, len(policy.Refs))}
	seen := make(map[contract.GitRefSelector]bool)
	for _, rule := range policy.Refs {
		valid := rule.Ref.Kind == "exact" && ValidRef(rule.Ref.Value) || rule.Ref.Kind == "prefix" && strings.HasSuffix(rule.Ref.Value, "/") && ValidRef(rule.Ref.Value+"x") && len(rule.Ref.Value) <= contract.GitRefBytes
		if !valid || seen[rule.Ref] || len(rule.Actions) == 0 || len(rule.Actions) > 3 {
			return contract.GitPolicy{}, ErrInvalid
		}
		seen[rule.Ref] = true
		actions := slices.Clone(rule.Actions)
		slices.Sort(actions)
		for i, action := range actions {
			if !slices.Contains([]string{"create", "update", "delete"}, action) || i > 0 && actions[i-1] == action {
				return contract.GitPolicy{}, ErrInvalid
			}
		}
		out.Refs = append(out.Refs, contract.GitRefRule{Ref: rule.Ref, Actions: actions})
	}
	slices.SortFunc(out.Refs, func(a, b contract.GitRefRule) int {
		return strings.Compare(a.Ref.Kind+":"+a.Ref.Value, b.Ref.Kind+":"+b.Ref.Value)
	})
	return out, nil
}

func Decode(raw []byte) (contract.GitPolicy, error) {
	var input struct {
		Version *int                   `json:"version"`
		Read    *bool                  `json:"read"`
		Refs    *[]contract.GitRefRule `json:"refs"`
	}
	if strictjson.Decode(raw, &input, strictjson.Options{MaxBytes: contract.GitPolicyBytes, MaxDepth: 8, RejectUnknownMembers: true}) != nil || input.Version == nil || input.Read == nil || input.Refs == nil {
		return contract.GitPolicy{}, ErrInvalid
	}
	return Normalize(contract.GitPolicy{Version: *input.Version, Read: *input.Read, Refs: *input.Refs})
}

func JSON(policy contract.GitPolicy) ([]byte, error) {
	p, err := Normalize(policy)
	if err != nil {
		return nil, err
	}
	return json.Marshal(p)
}

type Grant struct{ Policy contract.GitPolicy }
type RefAction struct{ Ref, Action string }

// Evaluate checks every action independently across an unordered union of
// allows. Read alone never authorizes mutation; an invalid loaded grant fails
// the whole evaluation, including grants that would not match this request.
func Evaluate(grants []Grant, actions []RefAction) (bool, error) {
	if len(grants) > contract.GitGrants || len(actions) > contract.GitRequestedRefs {
		return false, ErrInvalid
	}
	policies := make([]contract.GitPolicy, 0, len(grants))
	read := false
	for _, grant := range grants {
		p, err := Normalize(grant.Policy)
		if err != nil {
			return false, err
		}
		policies = append(policies, p)
		read = read || p.Read
	}
	if len(actions) == 0 {
		return read, nil
	}
	seen := make(map[string]bool)
	for _, action := range actions {
		if !ValidRef(action.Ref) || !slices.Contains([]string{"create", "update", "delete"}, action.Action) || seen[action.Ref] {
			return false, ErrInvalid
		}
		seen[action.Ref] = true
	}
	for _, action := range actions {
		allowed := false
		for _, policy := range policies {
			for _, rule := range policy.Refs {
				match := rule.Ref.Kind == "exact" && rule.Ref.Value == action.Ref || rule.Ref.Kind == "prefix" && strings.HasPrefix(action.Ref, rule.Ref.Value)
				allowed = allowed || match && slices.Contains(rule.Actions, action.Action)
			}
		}
		if !allowed {
			return false, nil
		}
	}
	return true, nil
}
