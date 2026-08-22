package runtimehost

func (e *LocalExecutor) runReadOnlyCapture(request Request, binding RuntimeBinding) {
	defer e.workers.Done()
	result, executionErr := e.executeOnce(e.ctx, request, binding)
	fingerprint := requestFingerprint(request)
	e.recordCompletionError(
		e.store.Complete(request.OperationID, fingerprint, result, executionErr),
	)
	e.submitMu.Lock()
	delete(e.active, request.OperationID)
	e.submitMu.Unlock()
}
