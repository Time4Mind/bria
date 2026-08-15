package domain

// A delivered-input acknowledgement can be committed after a fast provider
// has already written its final transcript event. It is not new activity, so
// preserve the original prompt timestamp for monotonic reconciliation.
func isDeliveredInputAcknowledgement(
	previous RuntimePhase,
	phase RuntimePhase,
	result *SessionOperationResult,
) bool {
	return previous == RuntimeRunning && phase == RuntimeRunning && result != nil &&
		result.Action == ActionSendInput && result.Status == OperationSucceeded
}
