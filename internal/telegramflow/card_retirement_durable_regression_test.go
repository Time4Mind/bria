package telegramflow_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

type a49CompletionPreparedEnqueuer interface {
	EnqueueCompletionPrepared(context.Context, uint64, telegramflow.Prepared) (coordinator.DurableOutboundReceipt, error)
}

type a49RetirementWire struct {
	mu            sync.Mutex
	events        []string
	failRetireFor int
	nextReceipt   int64
}

func (wire *a49RetirementWire) SendStatus(_ context.Context, operationID string, _ coordinator.Status) (coordinator.Receipt, error) {
	return wire.send("send:"+operationID, 0)
}

func (wire *a49RetirementWire) SendStatusWithKeyboard(_ context.Context, operationID string, _ coordinator.Status, _ *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	return wire.send("send:"+operationID, 0)
}

func (wire *a49RetirementWire) EditStatusWithKeyboard(_ context.Context, operationID string, status coordinator.Status, _ *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	return wire.send("edit:"+operationID, status.SourceMessageID)
}

func (wire *a49RetirementWire) send(event string, editReceipt int64) (coordinator.Receipt, error) {
	wire.mu.Lock()
	defer wire.mu.Unlock()
	wire.events = append(wire.events, event)
	if editReceipt > 0 {
		return coordinator.Receipt{MessageID: editReceipt}, nil
	}
	if wire.nextReceipt <= 0 {
		wire.nextReceipt = 900
	}
	receipt := wire.nextReceipt
	wire.nextReceipt++
	return coordinator.Receipt{MessageID: receipt}, nil
}

func (wire *a49RetirementWire) DeactivateInlineKeyboard(_ context.Context, _ string, _ int64, messageID int64) error {
	wire.mu.Lock()
	defer wire.mu.Unlock()
	wire.events = append(wire.events, "retire:"+strconv.FormatInt(messageID, 10))
	if wire.failRetireFor > 0 {
		wire.failRetireFor--
		return errors.New("retirement unavailable")
	}
	return nil
}

func (wire *a49RetirementWire) snapshot() []string {
	wire.mu.Lock()
	defer wire.mu.Unlock()
	return append([]string(nil), wire.events...)
}

type a49DurableRetirementFlow struct {
	ui         *telegramstate.FileStore
	operations *telegramflow.FileCallbackOperationStore
	outbound   *telegramflow.Sender
}

func openA49DurableRetirementFlow(t *testing.T, root string, now time.Time, wire *a49RetirementWire) a49DurableRetirementFlow {
	t.Helper()
	ui, err := telegramstate.OpenFileStore(filepath.Join(root, "ui.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := telegrampipeline.OpenFileCallbackRegistry(filepath.Join(root, "registry.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	operations, err := telegramflow.OpenFileCallbackOperationStore(filepath.Join(root, "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: newPresenter(t, now),
		CallbackRegistry: registry, UIState: ui, Messages: &messageHandler{}, Callbacks: &callbackExecutor{},
		Operations: operations, Sender: wire,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a49DurableRetirementFlow{ui: ui, operations: operations, outbound: outbound}
}

func setA49Carrier(t *testing.T, store telegramstate.Store, messageID int64) {
	t.Helper()
	if err := store.Update(context.Background(), func(state *telegramstate.State) error {
		state.ActiveSession = flowSessionID
		card, found := state.Card(flowSessionID)
		if !found {
			card = telegramstate.Card{SessionID: flowSessionID, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}}
		}
		card.Carrier = telegramstate.Carrier{ChatID: 42, MessageID: messageID}
		return state.SetCard(card)
	}); err != nil {
		t.Fatal(err)
	}
}

func prepareA49Final(t *testing.T, operationID string, presenterNow time.Time) telegramflow.Prepared {
	t.Helper()
	prepared, err := telegramflow.PrepareCompletion(operationID, flowSessionID, 42, true, "", telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{{Content: "final answer", Anchors: []string{"final"}, FinalStart: true, FinalOperationID: operationID}},
		View:  telegramui.PageView{Page: 1, Pages: 1, Anchor: "final", FollowLatest: true},
	}, false, nil, newPresenter(t, presenterNow))
	if err != nil {
		t.Fatal(err)
	}
	prepared.Card.FinalOperationID = operationID
	return prepared
}

func enqueueA49Completion(t *testing.T, outbound *telegramflow.Sender, sequence uint64, prepared telegramflow.Prepared) {
	t.Helper()
	enqueuer, ok := any(outbound).(a49CompletionPreparedEnqueuer)
	if !ok {
		t.Fatal("telegramflow.Sender does not implement EnqueueCompletionPrepared")
	}
	if _, err := enqueuer.EnqueueCompletionPrepared(context.Background(), sequence, prepared); err != nil {
		t.Fatal(err)
	}
}

func TestA49FinalRetryKeepsFirstPersistedRetirementTarget(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	root := t.TempDir()
	wire := &a49RetirementWire{failRetireFor: 1}
	flow := openA49DurableRetirementFlow(t, root, now, wire)
	setA49Carrier(t, flow.ui, 100)

	first := prepareA49Final(t, "final:a49-retry", now)
	var err error
	first, err = telegramflow.CapturePreviousCarrier(ctx, flow.ui, first, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.CardRetirementCaptured || first.CardRetirement == nil || first.CardRetirement.Carrier.MessageID != 100 {
		t.Fatalf("first retirement capture = %#v", first.CardRetirement)
	}
	enqueueA49Completion(t, flow.outbound, 7001, first)

	persisted, found, err := flow.operations.LoadStatus(ctx, first.OperationID)
	if err != nil || !found || persisted.Phase != telegramflow.StatusQueued || persisted.Prepared == nil ||
		!persisted.Prepared.CardRetirementCaptured || persisted.Prepared.CardRetirement == nil || persisted.Prepared.CardRetirement.Carrier.MessageID != 100 {
		t.Fatalf("persisted first target = %#v, found=%t, err=%v", persisted, found, err)
	}

	setA49Carrier(t, flow.ui, 200)
	flow = openA49DurableRetirementFlow(t, root, now, wire)
	retry := first
	retry.CardRetirementCaptured = false
	retry.CardRetirement = nil
	retry, err = telegramflow.CapturePreviousCarrier(ctx, flow.ui, retry, nil)
	if err != nil {
		t.Fatal(err)
	}
	if retry.CardRetirement == nil || retry.CardRetirement.Carrier.MessageID != 200 {
		t.Fatalf("retry did not observe replacement: %#v", retry.CardRetirement)
	}
	enqueueA49Completion(t, flow.outbound, 7001, retry)

	settled, found, err := flow.operations.LoadStatus(ctx, first.OperationID)
	if err != nil || !found || settled.Phase != telegramflow.StatusSuperseded {
		t.Fatalf("retry status = %#v, found=%t, err=%v", settled, found, err)
	}
	if got, want := wire.snapshot(), []string{"retire:100"}; !slices.Equal(got, want) {
		t.Fatalf("wire events = %v, want %v; replacement carrier must not be retired", got, want)
	}
	if receipt, confirmed, err := flow.outbound.ResolveStatusReceipt(ctx, first.OperationID); err != nil || !confirmed || receipt.MessageID <= 0 {
		t.Fatalf("settled retry receipt = %#v, confirmed=%t, err=%v", receipt, confirmed, err)
	}
}

func TestA49StaleRetirementSettlesAndDoesNotBlockNextQueuedStatusAfterReopen(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	root := t.TempDir()
	wire := &a49RetirementWire{}
	flow := openA49DurableRetirementFlow(t, root, now, wire)
	setA49Carrier(t, flow.ui, 100)

	stale := prepareA49Final(t, "final:a49-stale", now)
	if err := flow.ui.Update(ctx, func(state *telegramstate.State) error {
		card, _ := state.Card(flowSessionID)
		card.PendingFinalOperations = []string{stale.OperationID}
		return state.SetCard(card)
	}); err != nil {
		t.Fatal(err)
	}
	var err error
	stale, err = telegramflow.CapturePreviousCarrier(ctx, flow.ui, stale, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := flow.operations.EnqueueStatus(ctx, telegramflow.StatusOperation{
		ID: stale.OperationID, Sequence: 7101, Status: stale.Status, Keyboard: stale.Keyboard,
		Prepared: &stale, Phase: telegramflow.StatusQueued,
	}); err != nil {
		t.Fatal(err)
	}
	next := telegramflow.StatusOperation{
		ID: "status:a49-next", Sequence: 7102,
		Status: coordinator.Status{ConversationID: 42, Text: "next queued status"}, Phase: telegramflow.StatusQueued,
	}
	if _, _, err := flow.operations.EnqueueStatus(ctx, next); err != nil {
		t.Fatal(err)
	}
	setA49Carrier(t, flow.ui, 200)

	flow = openA49DurableRetirementFlow(t, root, now, wire)
	if err := flow.outbound.DeliverPendingStatuses(ctx, 10); err != nil {
		t.Fatal(err)
	}
	staleStatus, found, err := flow.operations.LoadStatus(ctx, stale.OperationID)
	if err != nil || !found || staleStatus.Phase != telegramflow.StatusSuperseded {
		t.Fatalf("stale status = %#v, found=%t, err=%v", staleStatus, found, err)
	}
	nextStatus, found, err := flow.operations.LoadStatus(ctx, next.ID)
	if err != nil || !found || nextStatus.Phase != telegramflow.StatusCommitted {
		t.Fatalf("next status = %#v, found=%t, err=%v", nextStatus, found, err)
	}
	if got, want := wire.snapshot(), []string{"send:" + next.ID}; !slices.Equal(got, want) {
		t.Fatalf("wire events = %v, want %v", got, want)
	}
	if receipt, confirmed, err := flow.outbound.ResolveStatusReceipt(ctx, stale.OperationID); err != nil || !confirmed || receipt.MessageID <= 0 {
		t.Fatalf("stale receipt = %#v, confirmed=%t, err=%v", receipt, confirmed, err)
	}
	state, err := flow.ui.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	card, _ := state.Card(flowSessionID)
	if len(card.PendingFinalOperations) != 0 {
		t.Fatalf("superseded final still blocks card updates: %v", card.PendingFinalOperations)
	}
}

func TestA49LegacyQueuedPreparedCannotRetireUnprovenCurrentCarrierAfterReopen(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	root := t.TempDir()
	wire := &a49RetirementWire{}
	flow := openA49DurableRetirementFlow(t, root, now, wire)
	setA49Carrier(t, flow.ui, 100)

	legacy := prepareA49Final(t, "final:a49-legacy", now)
	if legacy.CardRetirementCaptured || legacy.CardRetirement != nil {
		t.Fatalf("legacy fixture unexpectedly contains retirement tri-state: %#v", legacy)
	}
	if _, _, err := flow.operations.EnqueueStatus(ctx, telegramflow.StatusOperation{
		ID: legacy.OperationID, Sequence: 7201, Status: legacy.Status, Keyboard: legacy.Keyboard,
		Prepared: &legacy, Phase: telegramflow.StatusQueued,
	}); err != nil {
		t.Fatal(err)
	}
	serialized, err := os.ReadFile(filepath.Join(root, "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(serialized, []byte("CardRetirementCaptured")) || bytes.Contains(serialized, []byte("CardRetirement\"")) {
		t.Fatalf("legacy fixture unexpectedly serialized retirement tri-state: %s", serialized)
	}
	setA49Carrier(t, flow.ui, 200)

	flow = openA49DurableRetirementFlow(t, root, now, wire)
	if err := flow.outbound.DeliverPendingStatuses(ctx, 10); err != nil {
		t.Fatal(err)
	}
	status, found, err := flow.operations.LoadStatus(ctx, legacy.OperationID)
	if err != nil || !found || status.Phase != telegramflow.StatusSuperseded || status.Receipt != 200 {
		t.Fatalf("legacy status = %#v, found=%t, err=%v", status, found, err)
	}
	if got, want := wire.snapshot(), []string(nil); !slices.Equal(got, want) {
		t.Fatalf("wire events = %v, want %v", got, want)
	}
	if receipt, confirmed, err := flow.outbound.ResolveStatusReceipt(ctx, legacy.OperationID); err != nil || !confirmed || receipt.MessageID != 200 {
		t.Fatalf("legacy receipt = %#v, confirmed=%t, err=%v", receipt, confirmed, err)
	}
}

func TestA49LegacyQueuedPreparedRetiresRevisionMatchedCarrierAfterReopen(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	root := t.TempDir()
	wire := &a49RetirementWire{}
	flow := openA49DurableRetirementFlow(t, root, now, wire)
	setA49Carrier(t, flow.ui, 100)
	state, err := flow.ui.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	card, _ := state.Card(flowSessionID)
	legacy := prepareA49Final(t, "final:a49-legacy-matched", now)
	legacy.Card.ExpectedCarrierRevision = &card.CarrierRevision
	if _, _, err := flow.operations.EnqueueStatus(ctx, telegramflow.StatusOperation{
		ID: legacy.OperationID, Sequence: 7202, Status: legacy.Status, Keyboard: legacy.Keyboard,
		Prepared: &legacy, Phase: telegramflow.StatusQueued,
	}); err != nil {
		t.Fatal(err)
	}
	flow = openA49DurableRetirementFlow(t, root, now, wire)
	if err := flow.outbound.DeliverPendingStatuses(ctx, 10); err != nil {
		t.Fatal(err)
	}
	status, found, err := flow.operations.LoadStatus(ctx, legacy.OperationID)
	if err != nil || !found || status.Phase != telegramflow.StatusCommitted {
		t.Fatalf("matched legacy status = %#v, found=%t, err=%v", status, found, err)
	}
	if got, want := wire.snapshot(), []string{"retire:100", "send:" + legacy.OperationID}; !slices.Equal(got, want) {
		t.Fatalf("wire events = %v, want %v", got, want)
	}
}
