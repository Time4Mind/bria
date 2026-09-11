package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
)

// The real output journal stores the notification, not Prepared. This adapter
// separately exercises the public Prepared JSON format without inventing a
// durable final-card store or rewriting the production journal.
type finalCardJSONSender struct {
	*telegramflow.Sender
	decoded telegramflow.Prepared
}

func (s *finalCardJSONSender) Register(p telegramflow.Prepared) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var decoded telegramflow.Prepared
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	s.decoded = decoded
	return s.Sender.Register(decoded)
}

func (s *finalCardJSONSender) DeliverCompletionPrepared(ctx context.Context, sequence uint64, p telegramflow.Prepared) (coordinator.Receipt, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return coordinator.Receipt{}, err
	}
	if err := json.Unmarshal(data, &s.decoded); err != nil {
		return coordinator.Receipt{}, err
	}
	return s.Sender.DeliverCompletionPrepared(ctx, sequence, s.decoded)
}

func TestFinalCardDurableOutputReopenSkipsConfirmedDelivery(t *testing.T) {
	f := newNavigationFollowFixture(t)
	answer := "REOPEN_FINAL_BEGIN\n" + strings.Repeat("answer continuation\n", 300) + "REOPEN_FINAL_END"
	f.append("final", answer)
	session, err := f.store.Load(f.ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := session.FinishWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Replace(f.ctx, session, ready); err != nil {
		t.Fatal(err)
	}
	serialized := &finalCardJSONSender{Sender: f.out}
	completion := f.completion
	completion.Sender = serialized
	path := filepath.Join(t.TempDir(), "message-journal.json")
	open := func() (*messagejournal.Journal, *durableflow.Flow, durablecomposition.OutputCustody) {
		t.Helper()
		journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		flow, err := durableflow.New(journal, nil, durablecomposition.TelegramOutputSender{
			OwnerPrivateChatID: 42, Deliverer: completion,
		}, durableflow.Options{Owner: "final-reopen", LeaseDuration: time.Minute, Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		return journal, flow, durablecomposition.OutputCustody{Flow: flow, OwnerPrivateChatID: 42}
	}
	_, _, custody := open()
	notification := telegramcontroller.OutgoingNotification{SessionID: f.id, ConversationID: 42,
		OperationID: "final:exact-reopen", Kind: telegramcontroller.NotificationFinal, Payload: []byte(answer)}
	accepted, err := custody.AcceptOutput(f.ctx, notification)
	if err != nil || !accepted.Inserted {
		t.Fatalf("accept durable final: inserted=%t err=%v", accepted.Inserted, err)
	}
	journal, flow, _ := open() // Reconstruct the consumer from physical pending state.
	outputs, err := journal.Outputs(f.ctx, string(f.id))
	if err != nil || len(outputs) != 1 || outputs[0].Phase != messagejournal.OutputPending {
		t.Fatalf("pending final did not survive reopen: count=%d err=%v", len(outputs), err)
	}
	old, before := f.wire.last(), f.wire.snapshot()
	delivered, err := flow.DeliverNextOutput(f.ctx, string(f.id))
	if err != nil || delivered.State != durableflow.DeliveryConfirmed || delivered.Sequence != accepted.Sequence || delivered.OperationID != notification.OperationID || delivered.SessionID != string(f.id) {
		t.Fatalf("exact final delivery: state=%s err=%v", delivered.State, err)
	}
	packets := f.wire.snapshot()
	last := requireRetireThenNewRichCard(t, packets, len(before), old.ID)
	if last.ID == old.ID || !strings.Contains(last.Text, "REOPEN_FINAL_BEGIN") || strings.Contains(last.Text, "REOPEN_FINAL_END") {
		t.Fatal("reopened final did not create a distinct card at the answer beginning")
	}
	projection := serialized.decoded.Card.Projection.Card
	if projection.View.Page < 1 || projection.View.Page > len(projection.Pages) {
		t.Fatal("Prepared JSON lost selected final page")
	}
	page := projection.Pages[projection.View.Page-1]
	if !page.FinalStart || !strings.Contains(page.Content, "REOPEN_FINAL_BEGIN") {
		t.Fatal("Prepared JSON lost the typed first-final marker")
	}
	journal, flow, custody = open() // Confirmed receipt, not process-local dedup.
	outputs, err = journal.Outputs(f.ctx, string(f.id))
	if err != nil || len(outputs) != 1 || outputs[0].Phase != messagejournal.OutputConfirmed || outputs[0].Receipt != delivered.Receipt {
		t.Fatalf("confirmed exact receipt did not survive reopen: count=%d err=%v", len(outputs), err)
	}
	replayed, err := custody.AcceptOutput(f.ctx, notification)
	if err != nil || replayed.Inserted || replayed.Sequence != accepted.Sequence || replayed.OperationID != accepted.OperationID || replayed.SessionID != accepted.SessionID {
		t.Fatalf("exact durable replay was not idempotent: inserted=%t err=%v", replayed.Inserted, err)
	}
	if _, err := flow.DeliverNextOutput(f.ctx, string(f.id)); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("confirmed replay remained dispatchable: %v", err)
	}
	if countRichMessages(f.wire.snapshot()) != countRichMessages(before)+1 {
		t.Fatal("confirmed replay sent or edited a second card")
	}
	state, err := f.store.LoadTelegramUI(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	card, _ := state.Card(f.id)
	if card.Carrier.MessageID != last.ID || card.Page.Current != projection.View.Page {
		t.Fatal("replay changed the persisted new carrier or selected final page")
	}
}
