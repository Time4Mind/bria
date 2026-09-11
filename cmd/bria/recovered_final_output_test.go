package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/callbacktoken"
	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/recoverycomposition"
	"bria/internal/sessionruntime"
	"bria/internal/sessionsupervisor"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcompletioncomposition"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegrampromptcomposition"
	"bria/internal/telegramruntimecomposition"
)

type recoveredFinalReader func(context.Context, sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error)

func (r recoveredFinalReader) ReadAcceptedTurns(ctx context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	return r(ctx, request)
}

func TestRecoveredFinalReconciliationReopenUsesNormalOutputDispatcher(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	statePath, journalPath := filepath.Join(dir, "state.json"), filepath.Join(dir, "journal.json")
	store, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	id := domain.SessionID("11111111-1111-4111-9111-111111111111")
	starting, err := domain.NewStartingSession(id, "recovered-intent", "local", domain.ProviderCodex, "/synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
		t.Fatal(err)
	}
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "native-thread", Generation: 1}
	ready, err := starting.Ready(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(ctx, starting, ready); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActiveSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardCarrier(ctx, id, 42, 91); err != nil {
		t.Fatal(err)
	}
	const message = "recovered-message"
	if err := store.SetCardPrompt(ctx, id, message, "synthetic question"); err != nil {
		t.Fatal(err)
	}
	journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.EnqueueInput(ctx, string(id), message, []byte("synthetic question")); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.LeaseNextInput(ctx, string(id), "worker", time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.MarkInputAccepted(ctx, string(id), message, "worker"); err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	final := "RECOVERED_FINAL_BEGIN\n" + strings.Repeat("recovered answer continuation\n", 300) + "RECOVERED_FINAL_END"
	history, err := recoverycomposition.NewReconciler(recoveredFinalReader(func(_ context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
		if request.SessionID != id || request.Binding != binding {
			return sessionruntime.AcceptedTurnReconciliation{}, errors.New("recovery lost exact native binding")
		}
		return sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{{MessageID: message, TurnID: "native-turn", Outcome: sessionruntime.AcceptedTurnCompleted, Final: final}}}, nil
	}), store)
	if err != nil {
		t.Fatal(err)
	}
	reconciler := durablecomposition.AcceptedTurnReconciler{Flow: flow, FinalRestorer: durablecomposition.RecoveredFinalRestorer{History: store, Output: durablecomposition.OutputCustody{Flow: flow, OwnerPrivateChatID: 42}}, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history}}
	if _, err := reconciler.ReconcileAcceptedTurns(ctx, id, binding); err != nil {
		t.Fatal(err)
	}
	// Crash/reopen before dispatch: neither controller memory nor a manual final
	// deliverer call may be needed to find this recovered publication.
	store, err = storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	journal, err = messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := journal.Outputs(ctx, string(id))
	if err != nil || len(outputs) != 1 || outputs[0].OperationID != message+":final" || outputs[0].Phase != messagejournal.OutputPending || string(outputs[0].Payload) != final {
		t.Fatalf("recovered final has no exact durable dispatchable output: count=%d err=%v", len(outputs), err)
	}
	inputs, err := journal.Inputs(ctx, string(id))
	if err != nil || inputs[0].Phase != messagejournal.InputCompleted {
		t.Fatal("reconciled input did not commit after output custody")
	}
	state, err := store.LoadTelegramUI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	card, _ := state.Card(id)
	if len(card.PendingFinalOperations) != 1 || card.PendingFinalOperations[0] != message+":final" {
		t.Fatal("reopened final lost its publication fence")
	}
	completion, wire := recoveredFinalDispatcherFixture(t, store, ready)
	flow, err = durableflow.New(journal, nil, durablecomposition.TelegramOutputSender{OwnerPrivateChatID: 42, Deliverer: completion}, durableflow.Options{Owner: "restarted-worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	runRecoveredOutputDispatcher(t, ctx, flow, store, journal, id, message+":final")
	packets := wire.snapshot()
	if len(packets) != 1 || packets[0].Method != "sendRichMessage" || packets[0].ID == 91 || !strings.Contains(packets[0].Text, "RECOVERED_FINAL_BEGIN") || strings.Contains(packets[0].Text, "RECOVERED_FINAL_END") {
		t.Fatal("normal dispatcher did not publish one new card at recovered answer start")
	}
	store, err = storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.LoadTelegramUI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	card, _ = state.Card(id)
	if len(card.PendingFinalOperations) != 0 || card.Carrier.MessageID != packets[0].ID {
		t.Fatal("normal dispatch did not commit new carrier and clear exact fence")
	}
	journal, err = messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err = durableflow.New(journal, nil, durablecomposition.TelegramOutputSender{OwnerPrivateChatID: 42, Deliverer: completion}, durableflow.Options{Owner: "second-restart", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	reconciler.Flow = flow
	if _, err := reconciler.ReconcileAcceptedTurns(ctx, id, binding); err != nil {
		t.Fatal(err)
	}
	if _, err := flow.DeliverNextOutput(ctx, string(id)); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("confirmed recovered output became dispatchable: %v", err)
	}
	if len(wire.snapshot()) != 1 {
		t.Fatal("reopen replayed recovered final")
	}
}

func runRecoveredOutputDispatcher(t *testing.T, ctx context.Context, flow *durableflow.Flow, store *storage.SessionStore, journal *messagejournal.Journal, id domain.SessionID, operation string) {
	t.Helper()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done, reports := make(chan error, 1), make(chan error, 1)
	dispatcher := durablecomposition.OutputDispatcher{Flow: flow, Sessions: store, Wake: make(chan domain.SessionID), Report: func(err error) {
		select {
		case reports <- err:
		default:
		}
	}}
	go func() { done <- dispatcher.Run(runCtx) }()
	defer func() { stop(); <-done }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		outputs, err := journal.Outputs(ctx, string(id))
		if err != nil {
			t.Fatal(err)
		}
		for _, out := range outputs {
			if out.OperationID == operation && out.Phase == messagejournal.OutputConfirmed {
				return
			}
		}
		select {
		case err := <-reports:
			t.Fatalf("normal dispatcher failed: %v", err)
		case <-ctx.Done():
			t.Fatal("normal dispatcher never confirmed recovered final")
		case <-ticker.C:
		}
	}
}

type normalFinalEnqueueFailure struct {
	*messagejournal.Journal
	attempted chan struct{}
}

func (j normalFinalEnqueueFailure) EnqueueOutput(ctx context.Context, id, op, kind string, payload []byte) (messagejournal.Output, bool, error) {
	if kind == string(telegramcontroller.NotificationFinal) {
		j.attempted <- struct{}{}
		return messagejournal.Output{}, false, errors.New("synthetic final output write failed")
	}
	return j.Journal.EnqueueOutput(ctx, id, op, kind, payload)
}

func TestNormalProducerEnqueueFailureRejoinsRecoveredFinalDispatcher(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	old, store, path, ready := pageSelectionFixture(t)
	if err := old.Close(ctx); err != nil {
		t.Fatal(err)
	}
	id := ready.ID()
	if err := store.SetActiveSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardCarrier(ctx, id, 42, 91); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	fault := normalFinalEnqueueFailure{Journal: journal, attempted: make(chan struct{}, 1)}
	flow, err := durableflow.New(fault, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	provider := finalizingInputProvider{calls: make(chan telegramcontroller.DurableInputAcceptance, 2)}
	c, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, provider, archiveNotifier(nil), telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: store, TurnLifecycle: lifecycle, DurableOutput: durablecomposition.OutputCustody{Flow: flow, OwnerPrivateChatID: 42}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	const message = "normal-final-A"
	if _, err := flow.EnqueueInput(ctx, string(id), message, []byte("synthetic question")); err != nil {
		t.Fatal(err)
	}
	if _, err := flow.ProcessNextInput(ctx, string(id), durablecomposition.NewControllerInputProcessor(c)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fault.attempted:
	case <-ctx.Done():
		t.Fatal("normal producer never attempted final custody")
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	} // Join the real OnCompleted continuation before reopening.
	journal, err = messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := journal.Inputs(ctx, string(id))
	if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputAccepted {
		t.Fatal("normal producer lost accepted input after failed final enqueue")
	}
	store, err = storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	completion, wire := recoveredFinalDispatcherFixture(t, store, ready)
	flow, err = durableflow.New(journal, nil, durablecomposition.TelegramOutputSender{OwnerPrivateChatID: 42, Deliverer: completion}, durableflow.Options{Owner: "restarted", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	final := "final-" + message
	history, err := recoverycomposition.NewReconciler(recoveredFinalReader(func(context.Context, sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
		return sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{{MessageID: message, TurnID: "normal-native-turn", Outcome: sessionruntime.AcceptedTurnCompleted, Final: final}}}, nil
	}), store)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := ready.Binding()
	reconciler := durablecomposition.AcceptedTurnReconciler{Flow: flow, FinalRestorer: durablecomposition.RecoveredFinalRestorer{History: store, Output: durablecomposition.OutputCustody{Flow: flow, OwnerPrivateChatID: 42}}, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history}}
	if _, err := reconciler.ReconcileAcceptedTurns(ctx, id, binding); err != nil {
		t.Fatal(err)
	}
	runRecoveredOutputDispatcher(t, ctx, flow, store, journal, id, message+":final")
	packets := wire.snapshot()
	finalPackets := packetsContaining(packets, final)
	if len(finalPackets) != 1 || finalPackets[0].Method != "sendRichMessage" || finalPackets[0].ID == 91 {
		t.Fatal("normal failed final did not recover into one new card")
	}
	state, err := store.LoadTelegramUI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	card, _ := state.Card(id)
	if len(card.PendingFinalOperations) != 0 || card.Carrier.MessageID != finalPackets[0].ID || len(provider.calls) != 1 {
		t.Fatal("recovery lost final fence/carrier or replayed provider")
	}
}

func packetsContaining(packets []navigationFollowPacket, text string) []navigationFollowPacket {
	var filtered []navigationFollowPacket
	for _, packet := range packets {
		if strings.Contains(packet.Text, text) {
			filtered = append(filtered, packet)
		}
	}
	return filtered
}

func recoveredFinalDispatcherFixture(t *testing.T, store *storage.SessionStore, session domain.Session) (*telegramcompletioncomposition.Router, *navigationFollowHTTP) {
	t.Helper()
	c := archiveController(t, store, nil, telegramcontroller.Options{Recovered: []domain.Session{session}, UIState: store})
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	wire := &navigationFollowHTTP{next: 100}
	client, err := telegram.NewClient("123:recovered-synthetic", wire, telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	base, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close(context.Background()) })
	codec, err := callbacktoken.New(bytes.Repeat([]byte{29}, 32), nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, time.Now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cards := telegramruntimecomposition.SessionTelegramUIStore{State: store}
	adapter := telegramruntimecomposition.ControllerFlowAdapter{Controller: c}
	_, out, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(time.Now), UIState: cards, MessageUI: adapter, Callbacks: adapter, Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: base})
	if err != nil {
		t.Fatal(err)
	}
	completion := telegramcompletioncomposition.CompletionDeliverer{Controller: c, Cards: cards, Presenter: presenter, Sender: out, ConversationID: 42}
	router, err := telegramcompletioncomposition.NewRouter(completion)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.BindFinals(completion); err != nil {
		t.Fatal(err)
	}
	if err := router.BindPromptStatuses(telegrampromptcomposition.Deliverer{Controller: c, Cards: cards, Presenter: presenter, Sender: out}); err != nil {
		t.Fatal(err)
	}
	return router, wire
}
