package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramnotify"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramruntimecomposition"
	"bria/internal/telegramui"
)

// The barrier delays only delivery of an already prepared active final. User
// navigation, controller projection, disk state and Rich HTTP remain real.
func TestLatePreparedFinalCannotStealNewlySelectedSession(t *testing.T) {
	f := newNavigationFollowFixture(t)
	b := domain.SessionID("22222222-2222-4222-9222-222222222222")
	starting, err := domain.NewStartingSession(b, "late-final-b", "local", domain.ProviderCodex, "/synthetic-b")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.PutStartingIfAbsent(f.ctx, starting); err != nil {
		t.Fatal(err)
	}
	readyB, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "native-b", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Replace(f.ctx, starting, readyB); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetCardPrompt(f.ctx, b, "b-original", "B_ORIGINAL_PROMPT"); err != nil {
		t.Fatal(err)
	}
	// The shared fixture starts with only A recovered. Recompose with both live
	// sessions before the scenario; a disk-only B is deliberately not usable.
	if err := f.completion.Controller.(*telegramcontroller.Controller).Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	all, err := f.store.List(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := archiveController(t, f.store, nil, telegramcontroller.Options{Recovered: all, UIState: f.store})
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	if _, err := c.HandleSemanticAction(f.ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: f.id}); err != nil {
		t.Fatal(err)
	}
	client, err := telegram.NewClient("123:late-final-synthetic", f.wire, telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	base, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close(context.Background()) })
	adapter := telegramruntimecomposition.ControllerFlowAdapter{Controller: c}
	cards := telegramruntimecomposition.SessionTelegramUIStore{State: f.store}
	f.handler, f.out, err = telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: f.presenter, CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(time.Now), UIState: cards, MessageUI: adapter, Callbacks: adapter, Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: base})
	if err != nil {
		t.Fatal(err)
	}
	f.prompt.Controller, f.prompt.Sender = c, f.out
	f.completion.Controller, f.completion.Sender = c, f.out
	// This delivered keyboard contains the real signed B selector.
	f.deliver(telegramcontroller.NotificationPromptStatus, "a-with-b-selector")
	oldA := f.wire.last()
	var keyboard telegram.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(oldA.Keyboard), &keyboard); err != nil {
		t.Fatal(err)
	}
	var selectB string
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			decoded, err := f.presenter.DecodeCallback(button.CallbackData)
			if err == nil && decoded.Action == telegramui.ActionSelectSession && decoded.SessionID == string(b) {
				selectB = button.CallbackData
			}
		}
	}
	if selectB == "" {
		t.Fatal("actual A keyboard lacks signed B selector")
	}
	messageID := "prompt-" + string(f.id)
	operationID := messageID + ":final"
	answer := "A_FINAL_BEGIN\n" + strings.Repeat("A continuation line\n", 200) + "A_FINAL_END"
	if err := f.store.RestoreAcceptedFinal(f.ctx, f.id, messageID, answer); err != nil {
		t.Fatal(err)
	}
	runningA, err := f.store.Load(f.ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	readyA, err := runningA.FinishWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Replace(f.ctx, runningA, readyA); err != nil {
		t.Fatal(err)
	}

	gate := &lateFinalSendGate{Sender: f.out, entered: make(chan telegramflow.Prepared, 1), release: make(chan struct{}, 1)}
	defer close(gate.release)
	completion := f.completion
	completion.Sender = gate
	type result struct {
		receipt telegramnotify.DeliveryReceipt
		err     error
	}
	done := make(chan result, 1)
	go func() {
		r, err := completion.Deliver(f.ctx, telegramcontroller.Notification{Kind: telegramcontroller.NotificationFinal, OperationID: operationID, SessionID: f.id, ConversationID: 42, Text: answer}, operationID)
		done <- result{r, err}
	}()
	select {
	case prepared := <-gate.entered:
		if !prepared.Card.MakeActive || prepared.Card.FinalOperationID != operationID || prepared.Edit || prepared.Status.SourceMessageID != 0 {
			t.Fatalf("fixture did not delay an exact active new-card final: %+v", prepared.Card)
		}
	case r := <-done:
		t.Fatalf("final never reached send barrier: %v", r.err)
	case <-f.ctx.Done():
		t.Fatal("final send barrier timed out")
	}
	f.action(coordinator.Update{Kind: coordinator.UpdateCallback, Text: selectB, SourceMessageID: oldA.ID, CallbackQueryID: "late-final-select-b"})
	bCard := f.wire.last()
	if !strings.Contains(bCard.Text, "B_ORIGINAL_PROMPT") {
		t.Fatal("signed B selection did not deliver B")
	}
	before := f.wire.snapshot()
	gate.release <- struct{}{}
	select {
	case r := <-done:
		if r.err != nil || r.receipt.State != telegramnotify.DeliveryConfirmed {
			t.Fatalf("late A final: receipt=%+v err=%v", r.receipt, r.err)
		}
	case <-f.ctx.Done():
		t.Fatal("late A final did not finish")
	}
	packets := f.wire.snapshot()
	final := requireRetireThenNewRichCard(t, packets, len(before), oldA.ID)
	if final.ID == oldA.ID || final.ID == bCard.ID || !strings.Contains(final.Text, "A_FINAL_BEGIN") || strings.Contains(final.Text, "A_FINAL_END") {
		t.Fatalf("late final edited old carrier or selected wrong content: %+v", final)
	}
	// Reread durable storage: A's receipt must not restore A as active.
	state, err := f.store.LoadTelegramUI(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterA, foundA := state.Card(f.id)
	afterB, foundB := state.Card(b)
	if state.ActiveSession != b || !foundA || !foundB || afterA.Carrier.MessageID != final.ID || afterB.Carrier.MessageID != bCard.ID {
		t.Fatalf("late receipt stole persisted active/carrier: active=%s A=%+v B=%+v", state.ActiveSession, afterA.Carrier, afterB.Carrier)
	}
	_, activeB, err := f.completion.Controller.ProjectCompletion(f.ctx, b)
	if err != nil || !activeB {
		t.Fatalf("controller did not retain active B: %t %v", activeB, err)
	}
	projected, err := f.prompt.Controller.ProjectCurrent(f.ctx, "")
	if err != nil || projected.Card == nil || projected.Card.SessionID != b {
		t.Fatalf("controller current is not B: %+v err=%v", projected.Card, err)
	}
	if err := f.store.SetCardPrompt(f.ctx, b, "b-new", "B_FRESH_PROMPT"); err != nil {
		t.Fatal(err)
	}
	r, err := f.prompt.Deliver(f.ctx, telegramcontroller.Notification{Kind: telegramcontroller.NotificationPromptStatus, SessionID: b, ConversationID: 42}, "b-fresh-status")
	if err != nil || r.State != telegramnotify.DeliveryConfirmed || r.Suppressed {
		t.Fatalf("fresh B prompt suppressed: %+v err=%v", r, err)
	}
	latest := f.wire.last()
	if latest.Method != "editMessageText" || latest.ID != bCard.ID || !strings.Contains(latest.Text, "B_FRESH_PROMPT") || strings.Contains(latest.Text, "A_FINAL_BEGIN") {
		t.Fatalf("fresh B prompt did not update B carrier: %+v", latest)
	}
}

type lateFinalSendGate struct {
	*telegramflow.Sender
	prepared telegramflow.Prepared
	entered  chan telegramflow.Prepared
	release  chan struct{}
}

func (g *lateFinalSendGate) Register(p telegramflow.Prepared) error {
	g.prepared = p
	return g.Sender.Register(p)
}

func (g *lateFinalSendGate) SendStatusWithKeyboard(ctx context.Context, op string, s coordinator.Status, k *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	g.entered <- g.prepared
	select {
	case <-g.release:
	case <-ctx.Done():
		return coordinator.Receipt{}, ctx.Err()
	}
	return g.Sender.SendStatusWithKeyboard(ctx, op, s, k)
}
