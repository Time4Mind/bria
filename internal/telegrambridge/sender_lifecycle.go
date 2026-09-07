package telegrambridge

import "context"

// Close prevents new acknowledgements and waits for existing HTTP attempts AND
// their durable receipts. It creates no waiter goroutine on context timeout.
// Visible transport operations must already have stopped at the composition root.
func (sender *Sender) Close(ctx context.Context) error {
	if sender == nil {
		return nil
	}
	sender.ackMu.Lock()
	if !sender.ackClosed {
		sender.ackClosed = true
		if sender.ackActive == 0 {
			close(sender.ackDone)
		}
	}
	done := sender.ackDone
	sender.ackMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (sender *Sender) finishAcknowledgement() {
	sender.ackMu.Lock()
	defer sender.ackMu.Unlock()
	sender.ackActive--
	if sender.ackClosed && sender.ackActive == 0 {
		close(sender.ackDone)
	}
}
