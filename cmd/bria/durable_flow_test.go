package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

type durableProcessorStub struct {
	input  telegramcontroller.DurableLeasedInput
	inputs int
}

type notificationDelivererStub struct {
	notification telegramcontroller.Notification
	operationID  string
	receipt      telegramnotify.DeliveryReceipt
	err          error
	calls        int
}

func (stub *notificationDelivererStub) Deliver(_ context.Context, notification telegramcontroller.Notification, operationID string) (telegramnotify.DeliveryReceipt, error) {
	stub.calls++
	stub.notification = notification
	stub.operationID = operationID
	return stub.receipt, stub.err
}

func (stub *durableProcessorStub) ProcessDurableInput(
	ctx context.Context,
	input telegramcontroller.DurableLeasedInput,
	callbacks telegramcontroller.DurableInputCallbacks,
) (telegramcontroller.DurableInputProcessReceipt, error) {
	stub.input = input
	stub.inputs++
	if err := callbacks.OnAccepted(ctx, telegramcontroller.DurableInputAcceptance{
		SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence,
	}); err != nil {
		return telegramcontroller.DurableInputProcessReceipt{}, err
	}
	return telegramcontroller.DurableInputProcessReceipt{
		SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence,
		Accepted: true, Completion: telegramcontroller.DurableInputSucceeded,
	}, nil
}

type durableSessionStoreStub struct{ session domain.Session }

func (stub durableSessionStoreStub) List(context.Context) ([]domain.Session, error) {
	return []domain.Session{stub.session}, nil
}

func (stub durableSessionStoreStub) Load(context.Context, domain.SessionID) (domain.Session, error) {
	return stub.session, nil
}

func TestDurableControllerInputProcessorCommitsAcceptanceAndExactCompletion(t *testing.T) {
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	processor := &durableProcessorStub{}
	adapter := durablecomposition.NewControllerInputProcessor(processor)
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{
		Owner: "local-worker", LeaseDuration: time.Minute, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "123e4567-e89b-12d3-a456-426614174000"
	receipt, err := flow.EnqueueInput(context.Background(), sessionID, "telegram-update:9", []byte("hello"))
	if err != nil || receipt.Sequence == 0 {
		t.Fatalf("enqueue = %#v, %v", receipt, err)
	}

	processed, err := flow.ProcessNextInput(context.Background(), sessionID, adapter)
	if err != nil || processed.State != durableflow.InputProcessCompleted {
		t.Fatalf("process = %#v, %v", processed, err)
	}
	inputs, err := journal.Inputs(context.Background(), sessionID)
	if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputCompleted {
		t.Fatalf("journal inputs = %#v, %v", inputs, err)
	}
	if processor.input.SessionID != domain.SessionID(sessionID) || processor.input.Sequence != receipt.Sequence {
		t.Fatalf("processor input = %#v", processor.input)
	}
}

func TestDurableInputCustodyAcknowledgesOnlyExactJournalReceipt(t *testing.T) {
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{
		Owner: "local-worker", LeaseDuration: time.Minute, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	custody := durablecomposition.InputCustody{Flow: flow, Wake: make(chan domain.SessionID, 1)}
	want := telegramcontroller.SessionInput{
		SessionID: "123e4567-e89b-12d3-a456-426614174000",
		MessageID: "telegram-update:10", Payload: []byte("durable"),
		Attachments: []telegramcontroller.AttachmentRef{{
			Reference: "photo-custody-1", Size: 7,
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
	}
	got, err := custody.Accept(context.Background(), want)
	if err != nil || got.SessionID != want.SessionID || got.MessageID != want.MessageID || got.Sequence == 0 || !got.Inserted {
		t.Fatalf("Accept() = %#v, %v", got, err)
	}
	select {
	case id := <-custody.Wake:
		if id != want.SessionID {
			t.Fatalf("wake session = %q", id)
		}
	default:
		t.Fatal("durable acceptance did not wake dispatcher")
	}
	inputs, err := journal.Inputs(context.Background(), string(want.SessionID))
	if err != nil || len(inputs) != 1 || len(inputs[0].Attachments) != 1 ||
		inputs[0].Attachments[0].Reference != want.Attachments[0].Reference ||
		inputs[0].Attachments[0].Size != want.Attachments[0].Size ||
		inputs[0].Attachments[0].SHA256 != want.Attachments[0].SHA256 {
		t.Fatalf("durable attachment custody = %#v, %v", inputs, err)
	}
}

func TestDurableInputDispatcherDrainsReadySessionInSequence(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{
		Owner: "local-worker", LeaseDuration: time.Minute, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = domain.SessionID("123e4567-e89b-12d3-a456-426614174000")
	starting, err := domain.NewStartingSession(sessionID, "intent", "local", domain.ProviderCodex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{"telegram-update:1", "telegram-update:2"} {
		if _, err := flow.EnqueueInput(context.Background(), string(sessionID), messageID, []byte(messageID)); err != nil {
			t.Fatal(err)
		}
	}
	journal, err = messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err = durableflow.New(journal, nil, nil, durableflow.Options{
		Owner: "restarted-worker", LeaseDuration: time.Minute, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	processor := &durableProcessorStub{}
	dispatcher := durablecomposition.InputDispatcher{
		Flow: flow, Processor: durablecomposition.NewControllerInputProcessor(processor),
		Sessions: durableSessionStoreStub{session: ready},
	}
	if err := dispatcher.ProcessReadySession(context.Background(), sessionID); err != nil {
		t.Fatal(err)
	}
	inputs, err := journal.Inputs(context.Background(), string(sessionID))
	if err != nil || len(inputs) != 2 || inputs[0].Phase != messagejournal.InputCompleted || inputs[1].Phase != messagejournal.InputCompleted {
		t.Fatalf("inputs = %#v, %v", inputs, err)
	}
	if processor.inputs != 2 || processor.input.MessageID != "telegram-update:2" {
		t.Fatalf("processor calls = %d last=%#v", processor.inputs, processor.input)
	}
}

func TestDurableTelegramOutputSenderMapsConfirmedAndDefinitiveFailure(t *testing.T) {
	const sessionID = "123e4567-e89b-12d3-a456-426614174000"
	confirmed := &notificationDelivererStub{receipt: telegramnotify.DeliveryReceipt{
		OperationID: "turn:final", State: telegramnotify.DeliveryConfirmed,
		Parts: []telegramnotify.PartReceipt{{PartID: "turn:final:part:1-of-1", MessageID: 99}},
	}}
	sender := durablecomposition.TelegramOutputSender{OwnerPrivateChatID: 42, Deliverer: confirmed}
	request := durableflow.ProviderOutput{
		SessionID: sessionID, OperationID: "turn:final", Sequence: 7,
		Kind: string(telegramcontroller.NotificationFinal), Payload: []byte("done"),
	}
	result, err := sender.Deliver(context.Background(), request)
	if err != nil || result.State != durableflow.DeliveryConfirmed || result.Receipt == "" ||
		result.SessionID != sessionID || result.OperationID != request.OperationID || result.Sequence != 7 {
		t.Fatalf("confirmed delivery = %#v, %v", result, err)
	}
	if confirmed.notification.ConversationID != 42 || confirmed.notification.Text != "done" || confirmed.notification.SessionID != domain.SessionID(sessionID) {
		t.Fatalf("notification = %#v", confirmed.notification)
	}

	rejected := &notificationDelivererStub{
		receipt: telegramnotify.DeliveryReceipt{OperationID: "turn:error", State: telegramnotify.DeliveryFailed},
		err:     errors.New("authoritative rejection"),
	}
	sender.Deliverer = rejected
	request.OperationID = "turn:error"
	result, err = sender.Deliver(context.Background(), request)
	if err != nil || result.State != durableflow.DeliveryFailed || result.Receipt != "" {
		t.Fatalf("definitive delivery = %#v, %v", result, err)
	}
}

func TestDurableTelegramOutputAmbiguityIsSealedAndNeverAutoReplayed(t *testing.T) {
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	const (
		sessionID   = "123e4567-e89b-12d3-a456-426614174000"
		operationID = "turn:ambiguous"
	)
	deliverer := &notificationDelivererStub{
		receipt: telegramnotify.DeliveryReceipt{OperationID: operationID, State: telegramnotify.DeliveryUnknown},
		err:     context.DeadlineExceeded,
	}
	flow, err := durableflow.New(journal, nil, durablecomposition.TelegramOutputSender{
		OwnerPrivateChatID: 42, Deliverer: deliverer,
	}, durableflow.Options{Owner: "telegram", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.EnqueueOutput(context.Background(), sessionID, operationID, string(telegramcontroller.NotificationFinal), []byte("done")); err != nil {
		t.Fatal(err)
	}
	result, err := flow.DeliverNextOutput(context.Background(), sessionID)
	if !errors.Is(err, context.DeadlineExceeded) || result.State != durableflow.DeliveryUnknown {
		t.Fatalf("first delivery = %#v, %v", result, err)
	}
	outputs, err := journal.Outputs(context.Background(), sessionID)
	if err != nil || len(outputs) != 1 || outputs[0].Phase != messagejournal.OutputUnknown {
		t.Fatalf("outputs after ambiguity = %#v, %v", outputs, err)
	}
	if _, err := flow.DeliverNextOutput(context.Background(), sessionID); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("second delivery error = %v, want ErrNoAvailable", err)
	}
	if deliverer.calls != 1 {
		t.Fatalf("ambiguous operation delivered %d times, want once", deliverer.calls)
	}
}

func TestDurableOutputCustodyAcknowledgesExactJournalReceiptAndWakesDelivery(t *testing.T) {
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "telegram", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	wake := make(chan domain.SessionID, 1)
	custody := durablecomposition.OutputCustody{Flow: flow, Wake: wake, OwnerPrivateChatID: 42}
	input := telegramcontroller.OutgoingNotification{
		SessionID: "123e4567-e89b-12d3-a456-426614174000", OperationID: "turn:final",
		ConversationID: 42, Kind: telegramcontroller.NotificationFinal, Payload: []byte("done"),
	}
	receipt, err := custody.AcceptOutput(context.Background(), input)
	if err != nil || receipt.SessionID != input.SessionID || receipt.OperationID != input.OperationID || receipt.Sequence == 0 {
		t.Fatalf("AcceptOutput() = %#v, %v", receipt, err)
	}
	select {
	case got := <-wake:
		if got != input.SessionID {
			t.Fatalf("wake = %q", got)
		}
	default:
		t.Fatal("output acceptance did not wake delivery")
	}
}
