package telegrambridge_test

import (
	"bria/internal/telegrambridge"
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestSenderCloseJoinsReceiptAfterHTTPAckAndStopsNewLaunches(t *testing.T) {
	var calls atomic.Int32
	receiptStarted := make(chan struct{})
	releaseReceipt := make(chan struct{})
	receiptDone := make(chan struct{})
	client := mustTelegramClient(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(http.StatusOK, `{"ok":true,"result":true}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.BindCallbackAcknowledgements(callbackAcknowledgementRecorder{
		begin: func(string, string) (bool, error) { return true, nil },
		complete: func(string, string, telegrambridge.CallbackAcknowledgementState) error {
			close(receiptStarted)
			<-releaseReceipt
			close(receiptDone)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(releaseReceipt)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := sender.Close(ctx); err != nil {
			t.Errorf("cleanup sender: %v", err)
		}
	}()
	sender.AcknowledgeCallback(context.Background(), "status:51", "callback-51")
	select {
	case <-receiptStarted:
	case <-time.After(time.Second):
		t.Fatal("receipt not reached")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = sender.Close(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close skipped unfinished receipt: %v", err)
	}
	sender.AcknowledgeCallback(context.Background(), "status:52", "callback-52")
	if calls.Load() != 1 {
		t.Fatalf("closed sender launched ack: %d", calls.Load())
	}
	select {
	case <-receiptDone:
		t.Fatal("receipt finished before release")
	default:
	}
}
