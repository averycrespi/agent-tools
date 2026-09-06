package contract

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"time"
)

const (
	AuditRetention       = 65536
	AuditPageLimit       = 100
	AuditDetailBytes     = 2048
	AuditCursorBytes     = 2048
	AuditTimeRange       = 366 * 24 * time.Hour
	AuditTimestampLayout = "2006-01-02T15:04:05.000000000Z"
)

type AuditActorType string

const (
	AuditOperator AuditActorType = "operator"
	AuditSystem   AuditActorType = "system"
	AuditOffline  AuditActorType = "offline_maintenance"
)

type AuditCredential struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
}

type AuditActor struct {
	Type       AuditActorType   `json:"type"`
	Credential *AuditCredential `json:"credential"`
}

type AuditTarget struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type AuditSummary struct {
	ID            string           `json:"id"`
	Sequence      string           `json:"sequence"`
	Timestamp     string           `json:"timestamp"`
	Category      string           `json:"category"`
	Action        string           `json:"action"`
	Phase         string           `json:"phase"`
	Outcome       string           `json:"outcome"`
	Actor         AuditActor       `json:"actor"`
	Initiator     *AuditCredential `json:"initiator"`
	CorrelationID string           `json:"correlation_id"`
	Target        AuditTarget      `json:"target"`
}

// AuditDetail deliberately has no free-text or arbitrary JSON member.
type AuditDetail struct {
	Reason  *PublicReason `json:"reason"`
	Problem *string       `json:"problem"`
}

type AuditEvent struct {
	AuditSummary
	Detail AuditDetail `json:"detail"`
}

type AuditBoundary struct {
	ID        string `json:"id"`
	Sequence  string `json:"sequence"`
	Timestamp string `json:"timestamp"`
}

type AuditHistory struct {
	Generation     string         `json:"generation"`
	OldestRetained *AuditBoundary `json:"oldest_retained"`
	Pruned         bool           `json:"pruned"`
}

type AuditPage struct {
	Items      []AuditSummary `json:"items"`
	NextCursor *string        `json:"next_cursor"`
	History    AuditHistory   `json:"history"`
}

type AuditItem struct {
	Event   AuditEvent   `json:"event"`
	History AuditHistory `json:"history"`
}

// Empty filter members mean absent, never a wildcard or an inferred identity.
type AuditListQuery struct {
	Filters    AuditFilters
	Limit      int
	Cursor     string
	Generation string
}

type AuditItemQuery struct {
	Generation string
}

type AuditFilters struct {
	ActorType     AuditActorType `json:"actor_type"`
	CredentialID  string         `json:"credential_id"`
	Category      string         `json:"category"`
	Action        string         `json:"action"`
	TargetType    string         `json:"target_type"`
	TargetID      string         `json:"target_id"`
	Outcome       string         `json:"outcome"`
	CorrelationID string         `json:"correlation_id"`
	From          string         `json:"from"`
	Until         string         `json:"until"`
}

var (
	ErrInvalidAudit         = errors.New("invalid audit evidence")
	auditIDPattern          = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	auditFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
	auditActions            = map[string][]string{
		"admin_credential":  {"initialize", "create", "revoke", "rotate", "reset"},
		"admin_session":     {"sign_in", "logout"},
		"backup":            {"create", "delete", "restore"},
		"server":            {"create", "update", "delete", "reconcile"},
		"server_credential": {"replace", "disconnect", "invalidate"},
		"operation":         {"request", "activate", "reload", "retry", "refresh_catalog", "credential_replace", "disable", "delete", "disconnect_credentials", "schedule", "start", "finish", "recover"},
		"oauth":             {"create", "prepare", "authorize", "register", "publish_registration", "invalidate_registration", "await_callback", "begin_exchange", "exchange", "refresh", "install", "finish", "cancel", "expire", "supersede", "recover", "revoke"},
		"catalog":           {"refresh", "commit", "publish", "retire", "invalidate", "fence", "withdraw"},
		"keyring":           {"stage", "write", "commit", "activate", "fence", "delete", "cleanup"},
		"principal":         {"create", "update"},
		"agent_credential":  {"issue", "revoke", "invalidate"},
		"grant":             {"create", "update", "delete"},
		"grant_request":     {"approve", "reject"},
		"storage":           {"migrate", "recover", "verify"},
	}
)

func ValidAuditID(value string) bool { return auditIDPattern.MatchString(value) }

func AuditActorTypes() []AuditActorType {
	return []AuditActorType{AuditOperator, AuditSystem, AuditOffline}
}

func AuditActions(category string) []string { return slices.Clone(auditActions[category]) }

func AuditCategories() []string {
	values := make([]string, 0, len(auditActions))
	for category := range auditActions {
		values = append(values, category)
	}
	slices.Sort(values)
	return values
}

func AuditTargetTypes() []string {
	return []string{"installation", "admin_credential", "backup", "server", "operation", "auth_flow", "principal", "agent_credential", "grant", "grant_request", "descriptor"}
}

func AuditOutcomes() []string {
	return []string{"pending", "succeeded", "rejected", "failed", "unknown"}
}

func ValidAuditCredential(value *AuditCredential) bool {
	return value != nil && ValidAuditID(value.ID) && auditFingerprintPattern.MatchString(value.Fingerprint)
}

func ValidAuditTimestamp(value string) bool {
	parsed, err := time.Parse(AuditTimestampLayout, value)
	return err == nil && parsed.Year() >= 1970 && parsed.Format(AuditTimestampLayout) == value
}

func ValidateAuditEvent(event AuditEvent) error {
	sequence, err := strconv.ParseInt(event.Sequence, 10, 64)
	if err != nil || sequence < 1 || strconv.FormatInt(sequence, 10) != event.Sequence ||
		!ValidAuditID(event.ID) || !ValidAuditID(event.CorrelationID) || !ValidAuditTimestamp(event.Timestamp) ||
		!slices.Contains(AuditActions(event.Category), event.Action) ||
		!slices.Contains(AuditTargetTypes(), event.Target.Type) || !ValidAuditID(event.Target.ID) ||
		!slices.Contains(AuditActorTypes(), event.Actor.Type) || !slices.Contains(AuditOutcomes(), event.Outcome) {
		return ErrInvalidAudit
	}
	if (event.Phase != "attempt" && event.Phase != "outcome") || (event.Phase == "attempt") != (event.Outcome == "pending") {
		return ErrInvalidAudit
	}
	if event.Actor.Type == AuditOperator {
		if !ValidAuditCredential(event.Actor.Credential) || event.Initiator != nil {
			return ErrInvalidAudit
		}
	} else if event.Actor.Credential != nil {
		return ErrInvalidAudit
	}
	if event.Initiator != nil && (event.Actor.Type != AuditSystem || !ValidAuditCredential(event.Initiator)) {
		return ErrInvalidAudit
	}
	if event.Detail.Reason != nil {
		if _, err := ParsePublicReason(string(*event.Detail.Reason)); err != nil {
			return ErrInvalidAudit
		}
	}
	if event.Detail.Problem != nil {
		if _, ok := ProblemForCode(ProblemCode(*event.Detail.Problem)); !ok {
			return ErrInvalidAudit
		}
	}
	if event.Phase == "attempt" && (event.Detail.Reason != nil || event.Detail.Problem != nil) {
		return ErrInvalidAudit
	}
	return nil
}

func ValidateAuditFilters(filters AuditFilters) error {
	if filters.ActorType != "" && !slices.Contains(AuditActorTypes(), filters.ActorType) ||
		filters.Category != "" && !slices.Contains(AuditCategories(), filters.Category) ||
		filters.TargetType != "" && !slices.Contains(AuditTargetTypes(), filters.TargetType) ||
		filters.Outcome != "" && !slices.Contains(AuditOutcomes(), filters.Outcome) {
		return ErrInvalidAudit
	}
	if filters.Action != "" {
		valid := false
		for category, actions := range auditActions {
			if (filters.Category == "" || category == filters.Category) && slices.Contains(actions, filters.Action) {
				valid = true
			}
		}
		if !valid {
			return ErrInvalidAudit
		}
	}
	for _, id := range []string{filters.CredentialID, filters.TargetID, filters.CorrelationID} {
		if id != "" && !ValidAuditID(id) {
			return ErrInvalidAudit
		}
	}
	if (filters.From == "") != (filters.Until == "") {
		return ErrInvalidAudit
	}
	if filters.From != "" {
		if !ValidAuditTimestamp(filters.From) || !ValidAuditTimestamp(filters.Until) {
			return ErrInvalidAudit
		}
		from, _ := time.Parse(AuditTimestampLayout, filters.From)
		until, _ := time.Parse(AuditTimestampLayout, filters.Until)
		if !until.After(from) || until.Sub(from) > AuditTimeRange {
			return ErrInvalidAudit
		}
	}
	return nil
}
