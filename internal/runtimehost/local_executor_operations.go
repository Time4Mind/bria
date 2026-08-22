package runtimehost

// existingOperationReceipt preserves operation idempotency even when the
// runtime was retired after completing the operation. In particular, a close
// operation removes its session before a duplicate request can be submitted.
func (e *LocalExecutor) existingOperationReceipt(
	operationID string,
	fingerprint string,
) (Receipt, bool, error) {
	e.submitMu.Lock()
	defer e.submitMu.Unlock()
	return e.existingOperationReceiptLocked(operationID, fingerprint)
}

func (e *LocalExecutor) existingOperationReceiptLocked(
	operationID string,
	fingerprint string,
) (Receipt, bool, error) {
	if !e.accepting {
		return Receipt{}, true, ErrRuntimeShuttingDown
	}
	record, found, err := e.store.Lookup(operationID)
	if err != nil || !found {
		return Receipt{}, false, err
	}
	if record.Fingerprint != fingerprint {
		return Receipt{}, true, ErrOperationIDConflict
	}
	if record.State == OperationCompleted {
		return Receipt{
			OperationID: operationID, Accepted: true, Duplicate: true,
			Detail: "operation already completed",
		}, true, nil
	}
	if _, active := e.active[operationID]; !active {
		return Receipt{}, true, ErrOperationOutcomeUnknown
	}
	return Receipt{
		OperationID: operationID, Accepted: true, Duplicate: true,
		Detail: "operation already queued",
	}, true, nil
}
