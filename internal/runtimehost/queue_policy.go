package runtimehost

const controlQueueReserve = 4

// InputQueueLimitResolver supplies the replicated per-user request limit to
// the node that owns the process-local runtime FIFO. It deliberately keeps the
// limit out of Request so rolling clusters do not add a new wire field.
type InputQueueLimitResolver interface {
	InputQueueLimit(actorID int64) int
}

type defaultInputQueueLimitResolver struct{}

func (defaultInputQueueLimitResolver) InputQueueLimit(int64) int { return 5 }

func normalizeInputQueueLimit(limit int) int {
	switch limit {
	case 5, 10, 20:
		return limit
	default:
		return 5
	}
}

type queueAdmission struct {
	inputPending int
	totalPending int
	inputLimit   int
	totalLimit   int
	available    bool
}

func (s *localSession) queueAdmission(action Action, inputLimit int) (queueAdmission, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inputLimit = normalizeInputQueueLimit(inputLimit)
	admission := queueAdmission{
		totalPending: len(s.pending),
		inputLimit:   inputLimit,
		totalLimit:   inputLimit + controlQueueReserve,
		available:    !s.stopped && s.executor.ctx.Err() == nil,
	}
	for _, queued := range s.pending {
		if queued.request.Action == ActionSendInput {
			admission.inputPending++
		}
	}
	if !admission.available {
		return admission, false
	}
	if action == ActionSendInput && admission.inputPending >= admission.inputLimit {
		return admission, false
	}
	return admission, admission.totalPending < admission.totalLimit
}
