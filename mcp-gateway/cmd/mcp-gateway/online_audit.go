package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var auditGenerationPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func auditReadPath(options *onlineOptions, args []string) (string, error) {
	filters := map[string]string{}
	allowed := []string{"generation"}
	base := "/api/v1/audit-events"
	if len(args) == 1 {
		if !contract.ValidAuditID(args[0]) {
			return "", controlclient.ErrInvalidInput
		}
		base += "/" + args[0]
	} else {
		allowed = append(allowed, "actor_type", "credential_id", "category", "action", "target_type", "target_id", "outcome", "correlation_id", "from", "until")
	}
	for _, key := range allowed {
		if value := options.filters[strings.ReplaceAll(key, "_", "-")]; value != nil {
			if *value != "" {
				filters[key] = *value
			}
		}
	}
	if generation := filters["generation"]; generation != "" && !auditGenerationPattern.MatchString(generation) {
		return "", controlclient.ErrInvalidInput
	}
	if contract.ValidateAuditFilters(contract.AuditFilters{
		ActorType: contract.AuditActorType(filters["actor_type"]), CredentialID: filters["credential_id"], Category: filters["category"], Action: filters["action"], TargetType: filters["target_type"], TargetID: filters["target_id"], Outcome: filters["outcome"], CorrelationID: filters["correlation_id"], From: filters["from"], Until: filters["until"],
	}) != nil || len(options.cursor) > contract.AuditCursorBytes {
		return "", controlclient.ErrInvalidInput
	}
	return controlclient.BuildListPath(base, controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor, Filters: filters, AllowedFilters: allowed})
}

func runAuditRead(command *cobra.Command, options *onlineOptions, args []string) error {
	path, err := auditReadPath(options, args)
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("Audit filters are invalid. Supply from/until together as fixed UTC nanosecond timestamps, at most 366 days apart."))
	}
	return runOnlineRead(command, options, path, func(body []byte) (controlclient.Table, error) {
		var history contract.AuditHistory
		var table controlclient.Table
		if len(args) == 1 {
			var item contract.AuditItem
			if controlclient.DecodeExactResponse(body, &item) != nil || item.Event.ID != args[0] || contract.ValidateAuditEvent(item.Event) != nil {
				return table, controlclient.ErrResponseInvalid
			}
			history = item.History
			table = auditTable([]contract.AuditSummary{item.Event.AuditSummary})
			table.Notes = append(table.Notes, "Reason: "+auditReason(item.Event.Detail.Reason), "Problem: "+pointerText(item.Event.Detail.Problem))
		} else {
			var page contract.AuditPage
			if controlclient.DecodeExactResponse(body, &page) != nil || page.Items == nil || len(page.Items) > contract.AuditPageLimit || page.NextCursor != nil && (*page.NextCursor == "" || len(*page.NextCursor) > contract.AuditCursorBytes || len(page.Items) == 0) {
				return table, controlclient.ErrResponseInvalid
			}
			previous := int64(0)
			for _, event := range page.Items {
				sequence, _ := strconv.ParseInt(event.Sequence, 10, 64)
				if contract.ValidateAuditEvent(contract.AuditEvent{AuditSummary: event}) != nil || previous != 0 && sequence >= previous {
					return table, controlclient.ErrResponseInvalid
				}
				previous = sequence
			}
			history = page.History
			table = auditTable(page.Items)
			table.NextCursor = page.NextCursor
			if page.NextCursor != nil {
				table.Notes = append(table.Notes, "More matching retained events exist. Continue with --cursor and the same filters and --generation.")
			} else {
				table.Notes = append(table.Notes, "End of this matching retained traversal; not a complete or permanent record.")
			}
		}
		if !validAuditHistory(history) {
			return table, controlclient.ErrResponseInvalid
		}
		if expected := options.filters["generation"]; expected != nil && *expected != "" && *expected != history.Generation {
			return table, controlclient.ErrResponseInvalid
		}
		table.Notes = append(table.Notes, auditHistoryNotes(history)...)
		return table, nil
	})
}

func validAuditHistory(history contract.AuditHistory) bool {
	if !auditGenerationPattern.MatchString(history.Generation) {
		return false
	}
	if history.OldestRetained == nil {
		return !history.Pruned
	}
	boundary := history.OldestRetained
	sequence, err := strconv.ParseInt(boundary.Sequence, 10, 64)
	return err == nil && sequence > 0 && strconv.FormatInt(sequence, 10) == boundary.Sequence && contract.ValidAuditID(boundary.ID) && contract.ValidAuditTimestamp(boundary.Timestamp)
}

func auditReason(reason *contract.PublicReason) string {
	if reason == nil {
		return "-"
	}
	return string(*reason)
}

func auditTable(events []contract.AuditSummary) controlclient.Table {
	table := controlclient.Table{Headers: []string{"ID", "SEQUENCE", "TIME", "PERFORMER", "INITIATOR", "ACTION", "PHASE", "TARGET", "OUTCOME", "CORRELATION"}}
	for _, event := range events {
		performer := string(event.Actor.Type)
		if event.Actor.Credential != nil {
			performer += " " + auditCredential(event.Actor.Credential)
		}
		table.Rows = append(table.Rows, []string{event.ID, event.Sequence, event.Timestamp, performer, auditCredential(event.Initiator), event.Category + "." + event.Action, event.Phase, event.Target.Type + ":" + event.Target.ID, event.Outcome, event.CorrelationID})
	}
	return table
}

func auditCredential(value *contract.AuditCredential) string {
	if value == nil {
		return "-"
	}
	return value.ID + " fingerprint=" + value.Fingerprint
}

func auditHistoryNotes(history contract.AuditHistory) []string {
	boundary := "none (empty history)"
	if history.OldestRetained != nil {
		boundary = fmt.Sprintf("%s sequence=%s time=%s", history.OldestRetained.ID, history.OldestRetained.Sequence, history.OldestRetained.Timestamp)
	}
	return []string{
		"History generation: " + history.Generation,
		"Oldest retained: " + boundary,
		fmt.Sprintf("Retention: newest 65,536 events; older events pruned=%t.", history.Pruned),
		"Restore may replace local history and discard newer events. Pin --generation from the prior response; never combine different generations. On stale_cursor discard the traversal and restart page one, comparing generations. On audit_history_replaced discard prior-history state before restarting without --generation.",
		"System initiators are not performers; credential attribution does not identify a named human. Pending/unknown outcomes do not prove success, rollback, or permission to replay.",
	}
}
