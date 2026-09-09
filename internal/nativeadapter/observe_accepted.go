package nativeadapter

import (
	"context"
	"time"

	"bria/internal/runtimeprotocol"
)

// observeAccepted never calls Input or Key. Exact durable correlation is the
// only admission token; transcript offsets retain their identity on replay.
func (a *adapter) observeAccepted(ctx context.Context, request runtimeprotocol.ParentMessage) error {
	turnID := a.turnIDs[request.MessageID]
	status := a.receipts[request.MessageID]
	if a.active != nil || request.MessageID == "" || turnID == "" || (status != "unknown" && status != "completed" && status != "failed") {
		return runtimeprotocol.ErrProtocol
	}
	if a.reader != nil {
		if err := a.reader.Close(); err != nil {
			return err
		}
		a.reader = nil
	}
	if err := a.openReader(ctx); err != nil {
		return err
	}
	a.active = &activeInput{request: request, accepted: true, turnID: turnID, sent: time.Now()}
	a.final = ""
	a.steers = nil
	return a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeAccepted, RequestID: request.RequestID, MessageID: request.MessageID})
}
