package durablecomposition_test

import (
	"context"
	"errors"
	"fmt"
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
	attempts  *atomic.Int64
}

func (journal observedOutputJournal) LeaseNextOutput(ctx context.Context, sessionID, owner string, now time.Time, duration time.Duration) (messagejournal.Output, error) {
	output, err := journal.Journal.LeaseNextOutput(ctx, sessionID, owner, now, duration)
	if journal.attempts != nil {
		journal.attempts.Add(1)
	}
	select {
	case journal.attempted <- struct{}{}:
	default:
	}
	return output, err
}

func (journal observedOutputJournal) LeaseNextOutputForSessions(ctx context.Context, sessionIDs []string, owner string, now time.Time, duration time.Duration) (messagejournal.Output, error) {
	output, err := journal.Journal.LeaseNextOutputForSessions(ctx, sessionIDs, owner, now, duration)
	if journal.attempts != nil {
		journal.attempts.Add(1)
	}
	select {
	case journal.attempted <- struct{}{}:
	default:
	}
	return output, err
}

type outputSessionSet []domain.Session

func (sessions outputSessionSet) List(context.Context) ([]domain.Session, error) {
	return append([]domain.Session(nil), sessions...), nil
}

func (sessions outputSessionSet) Load(_ context.Context, id domain.SessionID) (domain.Session, error) {
	for _, session := range sessions {
		if session.ID() == id {
			return session, nil
		}
	}
	return domain.Session{}, errors.New("session not found")
}

type countedOutputSessions struct {
	outputSessionSet
	lists atomic.Int64
}

func (sessions *countedOutputSessions) List(ctx context.Context) ([]domain.Session, error) {
	sessions.lists.Add(1)
	return sessions.outputSessionSet.List(ctx)
}

func TestOutputDispatcherIdleSweepReadsJournalOncePerCycle(t *testing.T) {
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var sessions outputSessionSet
	for index := 0; index < 20; index++ {
		id := domain.SessionID(fmt.Sprintf("session-%02d", index))
		session, createErr := domain.NewStartingSessionAt(id, domain.IntentID("intent-"+string(id)), "computer", domain.ProviderCodex, "/work", time.Unix(1, 0), domain.SessionLifetimeNever)
		if createErr != nil {
			t.Fatal(createErr)
		}
		session, createErr = session.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread-" + string(id), Generation: 1}, time.Unix(2, 0))
		if createErr != nil {
			t.Fatal(createErr)
		}
		sessions = append(sessions, session)
	}
	attempts := &atomic.Int64{}
	flow, err := durableflow.New(observedOutputJournal{Journal: journal, attempted: make(chan struct{}, 1), attempts: attempts}, nil, outputSenderFunc(func(context.Context, durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
		return durableflow.DeliveryResult{}, errors.New("unexpected delivery")
	}), durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- (durablecomposition.OutputDispatcher{Flow: flow, Sessions: sessions, Wake: make(chan domain.SessionID), Report: func(err error) { t.Errorf("dispatch: %v", err) }}).Run(ctx)
	}()
	deadline := time.NewTimer(2 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	for attempts.Load() < 2 {
		select {
		case <-deadline.C:
			t.Fatal("dispatcher did not complete startup and one recovery sweep")
		case <-poll.C:
		}
	}
	poll.Stop()
	deadline.Stop()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("dispatcher shutdown = %v", err)
	}
	got := attempts.Load()
	t.Logf("idle journal reads across startup and one sweep = %d", got)
	if got > 3 {
		t.Fatalf("idle journal reads = %d, want at most one per sweep cycle", got)
	}
}

func TestOutputDispatcherFailureDoesNotBlockAnotherSessionInSameSweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	sessions := &countedOutputSessions{}
	for _, id := range []domain.SessionID{"session-a", "session-b"} {
		session, createErr := domain.NewStartingSessionAt(id, domain.IntentID("intent-"+string(id)), "computer", domain.ProviderCodex, "/work", time.Unix(1, 0), domain.SessionLifetimeNever)
		if createErr != nil {
			t.Fatal(createErr)
		}
		session, createErr = session.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread-" + string(id), Generation: 1}, time.Unix(2, 0))
		if createErr != nil {
			t.Fatal(createErr)
		}
		sessions.outputSessionSet = append(sessions.outputSessionSet, session)
		if _, _, err := journal.EnqueueOutput(ctx, string(id), "output-"+string(id), "commentary", []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	bSweep := make(chan int64, 1)
	flow, err := durableflow.New(journal, nil, outputSenderFunc(func(_ context.Context, output durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
		result := durableflow.DeliveryResult{SessionID: output.SessionID, OperationID: output.OperationID, Sequence: output.Sequence, State: durableflow.DeliveryFailed}
		if output.SessionID == "session-b" {
			result.State, result.Receipt = durableflow.DeliveryConfirmed, "telegram:b:confirmed"
			bSweep <- sessions.lists.Load()
		}
		return result, nil
	}), durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- (durablecomposition.OutputDispatcher{Flow: flow, Sessions: sessions, Wake: make(chan domain.SessionID), Report: func(err error) { t.Errorf("dispatch: %v", err) }}).Run(ctx)
	}()
	var sweep int64
	select {
	case sweep = <-bSweep:
	case <-ctx.Done():
		t.Fatal("session B was not delivered")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("dispatcher shutdown = %v", err)
	}
	if sweep != 1 {
		t.Fatalf("session B delivered on sweep %d, want first sweep", sweep)
	}
}

func TestOutputDispatcherTransportErrorDoesNotBlockAnotherSessionInSameSweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	sessions := &countedOutputSessions{}
	for _, id := range []domain.SessionID{"session-a", "session-b"} {
		session, createErr := domain.NewStartingSessionAt(id, domain.IntentID("intent-"+string(id)), "computer", domain.ProviderCodex, "/work", time.Unix(1, 0), domain.SessionLifetimeNever)
		if createErr != nil {
			t.Fatal(createErr)
		}
		session, createErr = session.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread-" + string(id), Generation: 1}, time.Unix(2, 0))
		if createErr != nil {
			t.Fatal(createErr)
		}
		sessions.outputSessionSet = append(sessions.outputSessionSet, session)
		if _, _, err := journal.EnqueueOutput(ctx, string(id), "output-"+string(id), "commentary", []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	bSweep := make(chan int64, 1)
	flow, err := durableflow.New(journal, nil, outputSenderFunc(func(_ context.Context, output durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
		if output.SessionID == "session-a" {
			return durableflow.DeliveryResult{}, errors.New("synthetic transport failure")
		}
		bSweep <- sessions.lists.Load()
		return durableflow.DeliveryResult{SessionID: output.SessionID, OperationID: output.OperationID, Sequence: output.Sequence, State: durableflow.DeliveryConfirmed, Receipt: "telegram:b:confirmed"}, nil
	}), durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- (durablecomposition.OutputDispatcher{Flow: flow, Sessions: sessions, Wake: make(chan domain.SessionID), Report: func(error) {}}).Run(ctx)
	}()
	var sweep int64
	select {
	case sweep = <-bSweep:
	case <-ctx.Done():
		t.Fatal("session B was not delivered")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("dispatcher shutdown = %v", err)
	}
	if sweep != 1 {
		t.Fatalf("session B delivered on sweep %d, want first sweep", sweep)
	}
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
