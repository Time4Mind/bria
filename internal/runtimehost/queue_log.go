package runtimehost

import (
	"github.com/Time4Mind/bria/internal/processlog"
)

func logRuntimeQueueBackpressure(request Request, admission queueAdmission) {
	processlog.Failuref(
		processlog.Service, processlog.FailureRateLimited,
		"bria runtime_queue: ref=%q action=%q outcome=backpressure input_pending=%d total_pending=%d input_limit=%d total_limit=%d",
		request.NodeID+"/"+request.SessionID, request.Action,
		admission.inputPending, admission.totalPending,
		admission.inputLimit, admission.totalLimit,
	)
}
