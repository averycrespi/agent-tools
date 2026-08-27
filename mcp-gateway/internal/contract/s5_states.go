package contract

type PolicyScope string

const (
	PolicyTool   PolicyScope = "tool"
	PolicyServer PolicyScope = "server"
)

func PolicyScopes() []PolicyScope                        { return []PolicyScope{PolicyTool, PolicyServer} }
func ParsePolicyScope(value string) (PolicyScope, error) { return parseClosed(value, PolicyScopes()) }

type GrantRequestState string

const (
	RequestPending   GrantRequestState = "pending"
	RequestApproved  GrantRequestState = "approved"
	RequestRejected  GrantRequestState = "rejected"
	RequestCancelled GrantRequestState = "cancelled"
)

func GrantRequestStates() []GrantRequestState {
	return []GrantRequestState{RequestPending, RequestApproved, RequestRejected, RequestCancelled}
}

func ParseGrantRequestState(value string) (GrantRequestState, error) {
	return parseClosed(value, GrantRequestStates())
}

type GrantRequestRejectionReason string

const (
	RejectionNotApproved    GrantRequestRejectionReason = "not_approved"
	RejectionExistingAccess GrantRequestRejectionReason = "existing_access"
	RejectionScopeTooBroad  GrantRequestRejectionReason = "scope_too_broad"
	RejectionPolicyConflict GrantRequestRejectionReason = "policy_conflict"
)

func GrantRequestRejectionReasons() []GrantRequestRejectionReason {
	return []GrantRequestRejectionReason{RejectionNotApproved, RejectionExistingAccess, RejectionScopeTooBroad, RejectionPolicyConflict}
}

func ParseGrantRequestRejectionReason(value string) (GrantRequestRejectionReason, error) {
	return parseClosed(value, GrantRequestRejectionReasons())
}

type DescriptorEvidenceState string

const (
	EvidenceCurrent DescriptorEvidenceState = "current"
	EvidenceRetired DescriptorEvidenceState = "retired"
)

func DescriptorEvidenceStates() []DescriptorEvidenceState {
	return []DescriptorEvidenceState{EvidenceCurrent, EvidenceRetired}
}

func ParseDescriptorEvidenceState(value string) (DescriptorEvidenceState, error) {
	return parseClosed(value, DescriptorEvidenceStates())
}

type TargetState string

const (
	TargetExtant  TargetState = "extant"
	TargetDeleted TargetState = "deleted"
)

func TargetStates() []TargetState                        { return []TargetState{TargetExtant, TargetDeleted} }
func ParseTargetState(value string) (TargetState, error) { return parseClosed(value, TargetStates()) }

type TargetActiveState string

const (
	TargetActiveCurrent     TargetActiveState = "current"
	TargetActiveStale       TargetActiveState = "stale"
	TargetActiveAbsent      TargetActiveState = "absent"
	TargetActiveUnavailable TargetActiveState = "unavailable"
)

func TargetActiveStates() []TargetActiveState {
	return []TargetActiveState{TargetActiveCurrent, TargetActiveStale, TargetActiveAbsent, TargetActiveUnavailable}
}

func ParseTargetActiveState(value string) (TargetActiveState, error) {
	return parseClosed(value, TargetActiveStates())
}

type TargetDurableState string

const (
	TargetDurableCurrent TargetDurableState = "current"
	TargetDurableRetired TargetDurableState = "retired"
	TargetDurableAbsent  TargetDurableState = "absent"
)

func TargetDurableStates() []TargetDurableState {
	return []TargetDurableState{TargetDurableCurrent, TargetDurableRetired, TargetDurableAbsent}
}

func ParseTargetDurableState(value string) (TargetDurableState, error) {
	return parseClosed(value, TargetDurableStates())
}

type CursorOutcome string

const (
	CursorOK      CursorOutcome = "ok"
	CursorInvalid CursorOutcome = "invalid_cursor"
	CursorStale   CursorOutcome = "stale_cursor"
)

func CursorOutcomes() []CursorOutcome { return []CursorOutcome{CursorOK, CursorInvalid, CursorStale} }
func ParseCursorOutcome(value string) (CursorOutcome, error) {
	return parseClosed(value, CursorOutcomes())
}

type CreateGrantRequestOutcome string

const (
	RequestCreated           CreateGrantRequestOutcome = "created"
	RequestExisting          CreateGrantRequestOutcome = "existing"
	RequestDenyConflict      CreateGrantRequestOutcome = "deny_conflict"
	RequestTargetUnavailable CreateGrantRequestOutcome = "target_unavailable"
	RequestLimitReached      CreateGrantRequestOutcome = "limit_reached"
)

func CreateGrantRequestOutcomes() []CreateGrantRequestOutcome {
	return []CreateGrantRequestOutcome{RequestCreated, RequestExisting, RequestDenyConflict, RequestTargetUnavailable, RequestLimitReached}
}

func ParseCreateGrantRequestOutcome(value string) (CreateGrantRequestOutcome, error) {
	return parseClosed(value, CreateGrantRequestOutcomes())
}

type GetGrantRequestOutcome string

const (
	RequestFound    GetGrantRequestOutcome = "found"
	RequestNotFound GetGrantRequestOutcome = "not_found"
)

func GetGrantRequestOutcomes() []GetGrantRequestOutcome {
	return []GetGrantRequestOutcome{RequestFound, RequestNotFound}
}

func ParseGetGrantRequestOutcome(value string) (GetGrantRequestOutcome, error) {
	return parseClosed(value, GetGrantRequestOutcomes())
}

type CancelGrantRequestOutcome string

const (
	RequestCancellationCancelled        CancelGrantRequestOutcome = "cancelled"
	RequestCancellationAlreadyCancelled CancelGrantRequestOutcome = "already_cancelled"
	RequestCancellationNotPending       CancelGrantRequestOutcome = "not_pending"
	RequestCancellationNotFound         CancelGrantRequestOutcome = "not_found"
)

func CancelGrantRequestOutcomes() []CancelGrantRequestOutcome {
	return []CancelGrantRequestOutcome{RequestCancellationCancelled, RequestCancellationAlreadyCancelled, RequestCancellationNotPending, RequestCancellationNotFound}
}

func ParseCancelGrantRequestOutcome(value string) (CancelGrantRequestOutcome, error) {
	return parseClosed(value, CancelGrantRequestOutcomes())
}
