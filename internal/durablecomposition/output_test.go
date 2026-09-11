package durablecomposition_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

type deliveryFunc func(context.Context, telegramcontroller.Notification, string) (telegramnotify.DeliveryReceipt, error)

func (function deliveryFunc) Deliver(ctx context.Context, notification telegramcontroller.Notification, operationID string) (telegramnotify.DeliveryReceipt, error) {
	return function(ctx, notification, operationID)
}

type outputSenderFunc func(context.Context, durableflow.ProviderOutput) (durableflow.DeliveryResult, error)

func (function outputSenderFunc) Deliver(ctx context.Context, output durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
	return function(ctx, output)
}

type observedOutputJournal struct {
	*messagejournal.Journal
	attempted chan struct{}
}

func (journal observedOutputJournal) LeaseNextOutput(ctx context.Context, sessionID, owner string, now time.Time, duration time.Duration) (messagejournal.Output, error) {
	output, err := journal.Journal.LeaseNextOutput(ctx, sessionID, owner, now, duration)
	select {
	case journal.attempted <- struct{}{}:
	default:
	}
	return output, err
}

func TestOutputCustodyNeverSupersedesOrderedPromptStates(t *testing.T) {
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{
		Owner: "local", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	custody := durablecomposition.OutputCustody{Flow: flow, Reader: journal, OwnerPrivateChatID: 42}
	sessionID := domain.SessionID("00000000-0000-4000-8000-000000000001")
	for _, notification := range []telegramcontroller.OutgoingNotification{
		{OperationID: "event:1", ConversationID: 42, SessionID: sessionID, Kind: telegramcontroller.NotificationCommentary, Payload: []byte("one")},
		{OperationID: "event:2", ConversationID: 42, SessionID: sessionID, Kind: telegramcontroller.NotificationPromptStatus, Payload: []byte("two")},
		{OperationID: "question:1", ConversationID: 42, SessionID: sessionID, Kind: telegramcontroller.NotificationQuestion, Payload: []byte("question")},
		{OperationID: "final:1", ConversationID: 42, SessionID: sessionID, Kind: telegramcontroller.NotificationFinal, Payload: []byte("final")},
	} {
		if _, err := custody.AcceptOutput(context.Background(), notification); err != nil {
			t.Fatal(err)
		}
	}
	outputs, err := journal.Outputs(context.Background(), string(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	want := []messagejournal.OutputPhase{
		messagejournal.OutputSuperseded, messagejournal.OutputPending,
		messagejournal.OutputPending, messagejournal.OutputPending,
	}
	if len(outputs) != len(want) {
		t.Fatalf("outputs = %#v", outputs)
	}
	for index := range want {
		if outputs[index].Phase != want[index] {
			t.Fatalf("output phases = %#v, want %#v", outputs, want)
		}
	}
}

func TestTelegramOutputSenderAcceptsExplicitlySuppressedStateWithoutSyntheticMessageReceipt(t *testing.T) {
	sender := durablecomposition.TelegramOutputSender{OwnerPrivateChatID: 42, Deliverer: deliveryFunc(func(_ context.Context, _ telegramcontroller.Notification, operationID string) (telegramnotify.DeliveryReceipt, error) {
		return telegramnotify.DeliveryReceipt{OperationID: operationID, State: telegramnotify.DeliveryConfirmed, Suppressed: true}, nil
	})}
	result, err := sender.Deliver(context.Background(), durableflow.ProviderOutput{
		SessionID: "00000000-0000-4000-8000-000000000001", OperationID: "event:1", Sequence: 1,
		Kind: string(telegramcontroller.NotificationCommentary), Payload: []byte("hidden"),
	})
	if err != nil || result.State != durableflow.DeliveryConfirmed || result.Receipt != "telegram:event:1:suppressed" {
		t.Fatalf("Deliver() = (%#v, %v)", result, err)
	}
}

func TestOutputCustodyWaitsForExactConfirmedDelivery(t *testing.T) {
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	sender := durablecomposition.TelegramOutputSender{OwnerPrivateChatID: 42, Deliverer: deliveryFunc(func(_ context.Context, _ telegramcontroller.Notification, operationID string) (telegramnotify.DeliveryReceipt, error) {
		return telegramnotify.DeliveryReceipt{OperationID: operationID, State: telegramnotify.DeliveryConfirmed, Parts: []telegramnotify.PartReceipt{{PartID: operationID + ":1/1", MessageID: 91}}}, nil
	})}
	flow, err := durableflow.New(journal, nil, sender, durableflow.Options{Owner: "local", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(1, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	custody := durablecomposition.OutputCustody{Flow: flow, Reader: journal, OwnerPrivateChatID: 42}
	sessionID := domain.SessionID("00000000-0000-4000-8000-000000000001")
	operationID := "telegram-update:1:prompt-status:preprocessed"
	if _, err := custody.AcceptOutput(context.Background(), telegramcontroller.OutgoingNotification{
		OperationID: operationID, ConversationID: 42, SessionID: sessionID, Kind: telegramcontroller.NotificationPromptStatus, Payload: []byte("state"),
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- custody.WaitOutputDelivery(context.Background(), sessionID, operationID) }()
	select {
	case err := <-done:
		t.Fatalf("wait returned before delivery: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if result, err := flow.DeliverNextOutput(context.Background(), string(sessionID)); err != nil || result.State != durableflow.DeliveryConfirmed {
		t.Fatalf("deliver = (%#v, %v)", result, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not observe confirmed delivery")
	}
}

func TestOutputDispatcherRetriesExpiredRestartLeaseWithoutWake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.EnqueueOutput(ctx, "s", "event:1", "commentary", []byte("ready")); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_000, 0)
	if _, err := journal.LeaseNextOutput(ctx, "s", "stopped-worker", base, time.Minute); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(base.UnixNano())
	delivered := make(chan durableflow.ProviderOutput, 1)
	sender := outputSenderFunc(func(_ context.Context, output durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
		delivered <- output
		return durableflow.DeliveryResult{
			SessionID: output.SessionID, OperationID: output.OperationID, Sequence: output.Sequence,
			State: durableflow.DeliveryConfirmed, Receipt: "telegram:event:1:confirmed",
		}, nil
	})
	attempted := make(chan struct{}, 1)
	flow, err := durableflow.New(observedOutputJournal{Journal: journal, attempted: attempted}, nil, sender, durableflow.Options{
		Owner: "restarted-worker", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(0, clock.Load()) },
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := domain.NewStartingSessionAt("s", "intent", "computer", domain.ProviderCodex, "/work", time.Now(), domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := durablecomposition.OutputDispatcher{
		Flow: flow, Sessions: readySessions{session: session}, Wake: make(chan domain.SessionID), Report: func(err error) { t.Errorf("dispatch: %v", err) },
	}
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()

	select {
	case <-attempted:
	case <-ctx.Done():
		t.Fatal("initial restart scan did not inspect the leased output")
	}
	select {
	case output := <-delivered:
		t.Fatalf("active old lease was delivered: %#v", output)
	default:
	}
	clock.Store(base.Add(2 * time.Minute).UnixNano())
	select {
	case output := <-delivered:
		if output.OperationID != "event:1" {
			t.Fatalf("delivered operation = %q", output.OperationID)
		}
	case <-ctx.Done():
		t.Fatal("expired output lease was not retried without a wake event")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("dispatcher shutdown = %v", err)
	}
	outputs, err := journal.Outputs(context.Background(), "s")
	if err != nil || len(outputs) != 1 || outputs[0].Phase != messagejournal.OutputConfirmed || outputs[0].Receipt != "telegram:event:1:confirmed" {
		t.Fatalf("persisted delivery = (%#v, %v)", outputs, err)
	}
}
