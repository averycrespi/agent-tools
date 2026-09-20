package invocation

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

const invocationTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

var invocationOpaqueIDPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

const invocationSelect = `SELECT insertion_sequence, id, principal_id, credential_id, credential_fingerprint,
	credential_revision, admitted_at, admission_class, requested_name, redacted_arguments,
	server_id, tool_id, upstream_name, descriptor_revision, descriptor_fingerprint,
	decision, authorization_revision, evaluated_at, grant_id, completed_at, terminal_class, failure_diagnostics
	FROM invocations`

type invocationScanner interface {
	Scan(...any) error
}

func (repository *Repository) ValidateStartup(ctx context.Context) error {
	return repository.view(ctx, func(transaction *sql.Tx) error {
		rows, err := transaction.QueryContext(ctx, invocationSelect+` ORDER BY insertion_sequence, id LIMIT ?`, repository.limit+1)
		if err != nil {
			return fmt.Errorf("read invocations for validation: %w", err)
		}
		defer func() { _ = rows.Close() }()
		var count, previousSequence int64
		for rows.Next() {
			record, err := scanInvocation(rows)
			if err != nil {
				return fmt.Errorf("scan invocation for validation: %w", err)
			}
			if count >= repository.limit {
				return invalidInvocationState("invocation capacity is exceeded")
			}
			count++
			if record.Sequence <= previousSequence || !validStoredInvocation(record) {
				return invalidInvocationState("invocation row is malformed")
			}
			previousSequence = record.Sequence
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate invocations for validation: %w", err)
		}
		return nil
	})
}

func validPreparedAdmission(prepared PreparedAdmission) bool {
	return validOpaqueInvocationID(prepared.InvocationID) && validAdmission(prepared.admission, prepared.AdmittedAt)
}

func validAdmission(admission Admission, admittedAt string) bool {
	admitted, validTime := parseCanonicalInvocationTimestamp(admittedAt)
	if !validTime || !validOpaqueInvocationID(admission.PrincipalID) || !validOpaqueInvocationID(admission.CredentialID) ||
		!validFingerprint(admission.CredentialFingerprint, 16) || !validPositiveRevision(admission.CredentialRevision) {
		return false
	}
	if _, err := contract.ParseInvocationAdmissionClass(string(admission.Class)); err != nil {
		return false
	}
	details := admission.MCP
	if details.RequestedName != nil && !validInvocationName(*details.RequestedName) || details.RedactedArguments != nil && !validRedactedArguments(details.RedactedArguments) {
		return false
	}
	hasCall := details.RequestedName != nil && details.RedactedArguments != nil
	switch admission.Class {
	case contract.AdmissionInvalidParams:
		return details.Route == nil && admission.Authorization == nil
	case contract.AdmissionUnknownTool:
		return hasCall && details.Route == nil && admission.Authorization == nil
	case contract.AdmissionInvalidArguments, contract.AdmissionAuthorizationUnavailable:
		return hasCall && validRouteEvidence(details.Route) && admission.Authorization == nil
	case contract.AdmissionEvaluated:
		return hasCall && validRouteEvidence(details.Route) && validAuthorizationEvidence(admission.Authorization, admitted)
	default:
		return false
	}
}

func validRouteEvidence(route *RouteEvidence) bool {
	return route != nil && validOpaqueInvocationID(route.Target.ServerID) && validOpaqueInvocationID(route.ToolID) &&
		route.Target.UpstreamName != nil && validInvocationName(route.Target.ToolName()) && validPositiveRevision(route.DescriptorRevision) && validFingerprint(route.DescriptorFingerprint, 64)
}

func validAuthorizationEvidence(evidence *activity.Authorization, admitted time.Time) bool {
	if evidence == nil || !validNonnegativeRevision(evidence.AuthorizationRevision) {
		return false
	}
	if _, err := contract.ParseAuthorizationDecision(string(evidence.Decision)); err != nil {
		return false
	}
	evaluated, ok := parseCanonicalInvocationTimestamp(evidence.EvaluatedAt)
	if !ok || evaluated.Before(admitted) {
		return false
	}
	switch evidence.Decision {
	case contract.DecisionAllow, contract.DecisionDeny:
		return evidence.GrantID != nil && validOpaqueInvocationID(*evidence.GrantID)
	case contract.DecisionBlock:
		return evidence.GrantID == nil
	default:
		return false
	}
}

func validStoredInvocation(record contract.InvocationAuditRecord) bool {
	if record.Diagnostics != nil && (record.TerminalClass == nil || !record.Diagnostics.ValidFor(*record.TerminalClass)) {
		return false
	}
	envelope, details, ok := storedEvidence(record)
	if !ok || !validOpaqueInvocationID(envelope.InvocationID) ||
		!validAdmission(Admission{Admission: envelope.Admission, MCP: details}, envelope.AdmittedAt) {
		return false
	}
	if envelope.Completion == nil {
		return true
	}
	if envelope.Authorization == nil || envelope.Authorization.Decision != contract.DecisionAllow {
		return false
	}
	if _, err := contract.ParseInvocationTerminalClass(string(envelope.Completion.Class)); err != nil {
		return false
	}
	completed, ok := parseCanonicalInvocationTimestamp(envelope.Completion.CompletedAt)
	evaluated, evaluatedOK := parseCanonicalInvocationTimestamp(envelope.Authorization.EvaluatedAt)
	return ok && evaluatedOK && !completed.Before(evaluated)
}

// The flat storage/public adapter must reject partial nullable groups before
// constructing typed evidence; normalization must never hide corrupt history.
func storedEvidence(record contract.InvocationAuditRecord) (activity.Envelope, MCPDetails, bool) {
	envelope := activity.Envelope{
		Identity: activity.Identity{InvocationID: record.InvocationID, AdmittedAt: record.AdmittedAt},
		Admission: activity.Admission{
			PrincipalID: record.PrincipalID, CredentialID: record.CredentialID, CredentialFingerprint: record.CredentialFingerprint,
			CredentialRevision: record.CredentialRevision, Class: record.AdmissionClass,
		},
	}
	details := MCPDetails{RequestedName: record.RequestedName}
	if record.RedactedArguments != nil {
		details.RedactedArguments = []byte(*record.RedactedArguments)
	}
	routePresent := record.ServerID != nil || record.ToolID != nil || record.UpstreamName != nil || record.DescriptorRevision != nil || record.DescriptorFingerprint != nil
	if routePresent {
		if record.ServerID == nil || record.ToolID == nil || record.UpstreamName == nil || record.DescriptorRevision == nil || record.DescriptorFingerprint == nil {
			return activity.Envelope{}, MCPDetails{}, false
		}
		details.Route = &RouteEvidence{Target: accesstarget.Tool(*record.ServerID, *record.UpstreamName), ToolID: *record.ToolID,
			DescriptorRevision: *record.DescriptorRevision, DescriptorFingerprint: *record.DescriptorFingerprint}
	}
	authorizationPresent := record.AuthorizationDecision != nil || record.AuthorizationRevision != nil || record.EvaluatedAt != nil || record.GrantID != nil
	if authorizationPresent {
		if record.AuthorizationDecision == nil || record.AuthorizationRevision == nil || record.EvaluatedAt == nil {
			return activity.Envelope{}, MCPDetails{}, false
		}
		envelope.Authorization = &activity.Authorization{Decision: *record.AuthorizationDecision, AuthorizationRevision: *record.AuthorizationRevision,
			EvaluatedAt: *record.EvaluatedAt, GrantID: record.GrantID}
	}
	if (record.CompletedAt == nil) != (record.TerminalClass == nil) {
		return activity.Envelope{}, MCPDetails{}, false
	}
	if record.CompletedAt != nil {
		envelope.Completion = &activity.Completion{CompletedAt: *record.CompletedAt, Class: *record.TerminalClass}
	}
	return envelope, details, true
}

func scanInvocation(scanner invocationScanner) (contract.InvocationAuditRecord, error) {
	var (
		record                                                           contract.InvocationAuditRecord
		credentialRevision                                               int64
		descriptorRevision, authorizationRevision                        sql.NullInt64
		requestedName, redactedArguments, serverID, toolID, upstreamName sql.NullString
		descriptorFingerprint, decision, evaluatedAt, grantID            sql.NullString
		completedAt, terminal, diagnostic                                sql.NullString
	)
	if err := scanner.Scan(
		&record.Sequence, &record.InvocationID, &record.PrincipalID, &record.CredentialID, &record.CredentialFingerprint,
		&credentialRevision, &record.AdmittedAt, &record.AdmissionClass, &requestedName, &redactedArguments,
		&serverID, &toolID, &upstreamName, &descriptorRevision, &descriptorFingerprint,
		&decision, &authorizationRevision, &evaluatedAt, &grantID, &completedAt, &terminal, &diagnostic,
	); err != nil {
		return contract.InvocationAuditRecord{}, err
	}
	record.CredentialRevision = strconv.FormatInt(credentialRevision, 10)
	record.RequestedName = nullStringPointer(requestedName)
	record.RedactedArguments = nullStringPointer(redactedArguments)
	record.ServerID = nullStringPointer(serverID)
	record.ToolID = nullStringPointer(toolID)
	record.UpstreamName = nullStringPointer(upstreamName)
	if descriptorRevision.Valid {
		value := strconv.FormatInt(descriptorRevision.Int64, 10)
		record.DescriptorRevision = &value
	}
	record.DescriptorFingerprint = nullStringPointer(descriptorFingerprint)
	if decision.Valid {
		value := contract.AuthorizationDecision(decision.String)
		record.AuthorizationDecision = &value
	}
	if authorizationRevision.Valid {
		value := strconv.FormatInt(authorizationRevision.Int64, 10)
		record.AuthorizationRevision = &value
	}
	record.EvaluatedAt = nullStringPointer(evaluatedAt)
	record.GrantID = nullStringPointer(grantID)
	record.CompletedAt = nullStringPointer(completedAt)
	if terminal.Valid {
		value := contract.InvocationTerminalClass(terminal.String)
		record.TerminalClass = &value
	}
	if diagnostic.Valid {
		if record.TerminalClass == nil {
			return contract.InvocationAuditRecord{}, invalidInvocationState("diagnostic without completion")
		}
		var err error
		record.Diagnostics, err = contract.ParseFailureDiagnostics([]byte(diagnostic.String), *record.TerminalClass)
		if err != nil {
			return contract.InvocationAuditRecord{}, invalidInvocationState("invalid failure diagnostics")
		}
	}
	return record, nil
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func validOpaqueInvocationID(value string) bool { return invocationOpaqueIDPattern.MatchString(value) }

func validFingerprint(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range []byte(value) {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func validInvocationName(value string) bool {
	return utf8.ValidString(value) && len(value) > 0 && int64(len(value)) <= invocationNameLimit()
}

func validRedactedArguments(value []byte) bool {
	if int64(len(value)) > argumentCaptureLimit() {
		return false
	}
	parsed, err := strictjson.ParseValue(value, strictjson.Options{MaxBytes: argumentCaptureLimit(), MaxDepth: jsonDepthLimit()})
	validType := parsed.Type == strictjson.ValueObject || parsed.Type == strictjson.ValueString && parsed.String == "[TRUNCATED]"
	if err != nil || !validType {
		return false
	}
	compact, err := strictjson.EncodeCompact(parsed)
	return err == nil && bytes.Equal(compact, value)
}

func validPositiveRevision(value string) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == value
}

func validNonnegativeRevision(value string) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed >= 0 && strconv.FormatInt(parsed, 10) == value
}

func canonicalInvocationTimestamp(value time.Time) (string, bool) {
	if value.Year() < 1 || value.Year() > 9999 || value.UnixMilli() < 0 || value.UnixMilli() > 1<<48-1 {
		return "", false
	}
	return value.UTC().Format(invocationTimestampLayout), true
}

func parseCanonicalInvocationTimestamp(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	parsed = parsed.UTC()
	return parsed, parsed.Format(invocationTimestampLayout) == value
}

func canonicalInvocationTime(value time.Time) string {
	result, _ := canonicalInvocationTimestamp(value)
	return result
}

func invocationLimit() int64      { return requiredLimit("invocation_audit_rows") }
func invocationNameLimit() int64  { return requiredLimit("external_tool_name_bytes") }
func argumentCaptureLimit() int64 { return requiredLimit("invocation_argument_capture_bytes") }
func invalidInvocationState(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidState, reason)
}

func requiredLimit(name string) int64 {
	limit, ok := contract.FixedLimitByName(name)
	if !ok {
		panic("missing invocation limit: " + name)
	}
	return limit.Maximum
}
