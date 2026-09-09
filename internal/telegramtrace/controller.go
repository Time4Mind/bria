package telegramtrace

import (
	"strconv"

	"bria/internal/controllertelemetry"
)

// Controller copies the value-only event into the existing bounded flow queue.
// The same Record HMAC domains join controller targets to card session references.
func Controller(event controllertelemetry.Event) Event {
	return Event{
		Stage: event.Stage.String(), OperationID: event.OperationID,
		SessionID: event.SessionID, Time: event.Time, Result: event.Outcome.String(),
		controllerEvent: event, hasControllerEvent: true,
	}
}

func controllerFields(fields map[string]string, event controllertelemetry.Event, ref func(string, string) string) {
	fields["stage"] = event.Stage.String()
	fields["selection_reason"] = event.Reason.String()
	fields["selection_outcome"] = event.Outcome.String()
	for field, value := range map[string]string{
		"parent_operation_ref": ref("operation", event.ParentOperationID),
		"node_ref":             ref("node", event.NodeID),
		"previous_session_ref": ref("session", event.PreviousSessionID),
		"target_session_ref":   ref("session", event.TargetSessionID),
		"provider_session_ref": ref("provider_session", event.ProviderSessionID),
		"approval_ref":         ref("approval", event.ApprovalFingerprint),
	} {
		if value != "" {
			fields[field] = value
		}
	}
	if event.CandidatesKnown {
		fields["candidate_count"] = strconv.FormatUint(event.CandidateCount, 10)
	}
	if event.Generation > 0 {
		fields["generation"] = strconv.FormatUint(event.Generation, 10)
	}
}
