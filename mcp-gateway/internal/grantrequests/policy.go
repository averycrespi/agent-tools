package grantrequests

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

const DedupeVersion int64 = 1

const maximumDedupeBytes = 16384

var (
	ErrInvalidPolicy    = errors.New("grant request policy is invalid")
	ErrPolicyBroadening = errors.New("approved policy is not a narrowing")
	opaqueIDPattern     = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
)

type ResolvedTarget struct {
	ServerID     string
	UpstreamName *string
}

type DedupeIdentity struct {
	Version int64
	Bytes   []byte
}

type CompiledPolicy struct {
	value                contract.Policy
	constraint           *authorization.CompiledConstraint
	durationSeconds      int64
	durationSecondsValid bool
}

func CompilePolicy(input contract.Policy) (CompiledPolicy, error) {
	if !utf8.ValidString(input.Target) || len(input.Target) < 1 || int64(len(input.Target)) > fixedLimit("grant_request_target_bytes") {
		return CompiledPolicy{}, ErrInvalidPolicy
	}
	if _, err := contract.ParsePolicyScope(string(input.Scope)); err != nil {
		return CompiledPolicy{}, ErrInvalidPolicy
	}
	compiled := CompiledPolicy{value: input}
	if input.Constraint != nil {
		contents := append(json.RawMessage(nil), (*input.Constraint)...)
		constraint, err := authorization.CompileConstraint(contents)
		if err != nil {
			return CompiledPolicy{}, ErrInvalidPolicy
		}
		compiled.constraint = &constraint
		compiled.value.Constraint = &contents
	}
	switch input.Scope {
	case contract.PolicyTool:
		if input.FutureToolsAcknowledged {
			return CompiledPolicy{}, ErrInvalidPolicy
		}
	case contract.PolicyServer:
		if input.Constraint != nil || !input.FutureToolsAcknowledged {
			return CompiledPolicy{}, ErrInvalidPolicy
		}
	}
	if input.DurationSeconds != nil {
		text := *input.DurationSeconds
		seconds, err := strconv.ParseInt(text, 10, 64)
		if err != nil || strconv.FormatInt(seconds, 10) != text ||
			seconds < contract.GrantRequestDurationMinimumSeconds || seconds > contract.GrantRequestDurationMaximumSeconds {
			return CompiledPolicy{}, ErrInvalidPolicy
		}
		compiled.durationSeconds = seconds
		compiled.durationSecondsValid = true
		copied := text
		compiled.value.DurationSeconds = &copied
	}
	return compiled, nil
}

func (policy CompiledPolicy) Contract() contract.Policy {
	result := policy.value
	if result.Constraint != nil {
		contents := append(json.RawMessage(nil), (*result.Constraint)...)
		result.Constraint = &contents
	}
	if result.DurationSeconds != nil {
		text := *result.DurationSeconds
		result.DurationSeconds = &text
	}
	return result
}

func (policy CompiledPolicy) Scope() contract.PolicyScope { return policy.value.Scope }
func (policy CompiledPolicy) Target() string              { return policy.value.Target }
func (policy CompiledPolicy) FutureToolsAcknowledged() bool {
	return policy.value.FutureToolsAcknowledged
}

func (policy CompiledPolicy) ConstraintJSON() *json.RawMessage {
	return policy.Contract().Constraint
}

func (policy CompiledPolicy) DurationSeconds() (int64, bool) {
	return policy.durationSeconds, policy.durationSecondsValid
}

func (policy CompiledPolicy) ExpiresAt(approvedAt time.Time) *time.Time {
	if !policy.durationSecondsValid {
		return nil
	}
	expires := approvedAt.UTC().Add(time.Duration(policy.durationSeconds) * time.Second)
	return &expires
}

func CanonicalDedupeIdentity(policy CompiledPolicy, target ResolvedTarget) (DedupeIdentity, error) {
	if !validResolvedTarget(policy, target) {
		return DedupeIdentity{}, ErrInvalidPolicy
	}
	var output bytes.Buffer
	output.WriteString("MGWGRQ1\x00")
	writeBytes(&output, []byte(target.ServerID))
	if target.UpstreamName == nil {
		output.WriteByte(0)
	} else {
		output.WriteByte(1)
		writeBytes(&output, []byte(*target.UpstreamName))
	}
	if policy.constraint == nil {
		output.WriteByte(0)
	} else {
		output.WriteByte(1)
		atoms := policy.constraint.Atoms()
		sort.Slice(atoms, func(left, right int) bool { return atoms[left].Pointer < atoms[right].Pointer })
		output.WriteString(strconv.Itoa(len(atoms)))
		output.WriteByte(':')
		for _, atom := range atoms {
			writeBytes(&output, []byte(atom.Pointer))
			switch atom.Type {
			case authorization.ConstraintNull:
				output.WriteByte(0)
			case authorization.ConstraintBoolean:
				output.WriteByte(1)
				if atom.Boolean {
					output.WriteByte(1)
				} else {
					output.WriteByte(0)
				}
			case authorization.ConstraintString:
				output.WriteByte(2)
				writeBytes(&output, []byte(atom.String))
			case authorization.ConstraintNumber:
				output.WriteByte(3)
				writeBytes(&output, []byte(atom.Number))
			default:
				return DedupeIdentity{}, ErrInvalidPolicy
			}
		}
	}
	if policy.durationSecondsValid {
		output.WriteByte(1)
		output.WriteString(strconv.FormatInt(policy.durationSeconds, 10))
		output.WriteByte(':')
	} else {
		output.WriteByte(0)
	}
	if policy.value.FutureToolsAcknowledged {
		output.WriteByte(1)
	} else {
		output.WriteByte(0)
	}
	if output.Len() > maximumDedupeBytes {
		return DedupeIdentity{}, ErrInvalidPolicy
	}
	return DedupeIdentity{Version: DedupeVersion, Bytes: append([]byte(nil), output.Bytes()...)}, nil
}

func ValidateNarrowing(submitted CompiledPolicy, submittedTarget ResolvedTarget, approved CompiledPolicy, approvedTarget ResolvedTarget) error {
	if !validResolvedTarget(submitted, submittedTarget) || !validResolvedTarget(approved, approvedTarget) ||
		submittedTarget.ServerID != approvedTarget.ServerID {
		return ErrPolicyBroadening
	}
	switch submitted.Scope() {
	case contract.PolicyServer:
		if approved.Scope() == contract.PolicyServer {
			if approvedTarget.UpstreamName != nil {
				return ErrPolicyBroadening
			}
		} else if approved.Scope() != contract.PolicyTool || approvedTarget.UpstreamName == nil {
			return ErrPolicyBroadening
		}
	case contract.PolicyTool:
		if approved.Scope() != contract.PolicyTool || submittedTarget.UpstreamName == nil || approvedTarget.UpstreamName == nil ||
			*submittedTarget.UpstreamName != *approvedTarget.UpstreamName {
			return ErrPolicyBroadening
		}
		if !constraintRetained(submitted.constraint, approved.constraint) {
			return ErrPolicyBroadening
		}
	default:
		return ErrPolicyBroadening
	}
	if submitted.durationSecondsValid {
		if !approved.durationSecondsValid || approved.durationSeconds > submitted.durationSeconds {
			return ErrPolicyBroadening
		}
	}
	return nil
}

func validResolvedTarget(policy CompiledPolicy, target ResolvedTarget) bool {
	if !opaqueIDPattern.MatchString(target.ServerID) {
		return false
	}
	if target.UpstreamName != nil && (!utf8.ValidString(*target.UpstreamName) || len(*target.UpstreamName) < 1 || int64(len(*target.UpstreamName)) > fixedLimit("grant_request_target_bytes")) {
		return false
	}
	return policy.Scope() == contract.PolicyTool && target.UpstreamName != nil ||
		policy.Scope() == contract.PolicyServer && target.UpstreamName == nil
}

func constraintRetained(submitted, approved *authorization.CompiledConstraint) bool {
	if submitted == nil {
		return true
	}
	if approved == nil {
		return false
	}
	approvedAtoms := make(map[authorization.ConstraintAtom]struct{}, len(approved.Atoms()))
	for _, atom := range approved.Atoms() {
		approvedAtoms[atom] = struct{}{}
	}
	for _, atom := range submitted.Atoms() {
		if _, present := approvedAtoms[atom]; !present {
			return false
		}
	}
	return true
}

func writeBytes(output *bytes.Buffer, value []byte) {
	output.WriteString(strconv.Itoa(len(value)))
	output.WriteByte(':')
	output.Write(value)
}

func fixedLimit(name string) int64 {
	limit, ok := contract.FixedLimitByName(name)
	if !ok {
		panic("missing contract limit: " + name)
	}
	return limit.Maximum
}
