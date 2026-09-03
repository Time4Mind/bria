package telegramrecoverycomposition_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramrecoverycomposition"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

var errVerticalComplete = errors.New("vertical complete")

type verticalSource struct {
	transport *verticalTransport
	original  coordinator.Update
}

func (source *verticalSource) Bootstrap(context.Context) (int64, error) {
	return source.original.ID, nil
}
func (source *verticalSource) Poll(_ context.Context, offset int64) ([]coordinator.Update, error) {
	switch offset {
	case source.original.ID:
		return []coordinator.Update{source.original}, nil
	case source.original.ID + 1:
		return []coordinator.Update{{ID: offset, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
			Text: source.transport.recoveryToken, CallbackQueryID: "recovery-query", SourceMessageID: 500}}, nil
	case source.original.ID + 2:
		return []coordinator.Update{{ID: offset, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
			Text: source.transport.recoveryToken, CallbackQueryID: "double-click", SourceMessageID: 500}}, nil
	default:
		return nil, errVerticalComplete
	}
}

type verticalStore struct {
	mu       sync.Mutex
	revision uint64
	value    coordinator.Checkpoint
}

func (store *verticalStore) Load(context.Context) (coordinator.StoredCheckpoint, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.revision == 0 {
		return coordinator.StoredCheckpoint{}, false, nil
	}
	return coordinator.StoredCheckpoint{Revision: store.revision, Checkpoint: store.value}, true, nil
}
func (store *verticalStore) Save(_ context.Context, expected uint64, value coordinator.Checkpoint) (coordinator.StoredCheckpoint, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if expected != store.revision {
		return coordinator.StoredCheckpoint{}, errors.New("CAS conflict")
	}
	store.revision++
	store.value = value
	return coordinator.StoredCheckpoint{Revision: store.revision, Checkpoint: store.value}, nil
}

type readyStub struct{}

func (readyStub) Ready(context.Context, coordinator.Checkpoint) error { return nil }

type verticalTransport struct {
	recoveryToken string
	sends         int
	edits         int
}

func (transport *verticalTransport) SendStatus(_ context.Context, _ string, _ coordinator.Status) (coordinator.Receipt, error) {
	transport.sends++
	return coordinator.Receipt{MessageID: int64(600 + transport.sends)}, nil
}
func (transport *verticalTransport) SendStatusWithKeyboard(_ context.Context, _ string, _ coordinator.Status, keyboard *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	transport.sends++
	if keyboard == nil || len(*keyboard) == 0 || len((*keyboard)[0]) < 1 {
		return coordinator.Receipt{}, errors.New("missing recovery keyboard")
	}
	transport.recoveryToken = (*keyboard)[0][0].CallbackData
	return coordinator.Receipt{MessageID: 500}, nil
}
func (transport *verticalTransport) EditStatusWithKeyboard(_ context.Context, _ string, status coordinator.Status, _ *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	transport.edits++
	return coordinator.Receipt{MessageID: status.SourceMessageID}, nil
}

type currentCardProjector struct {
	requests []telegramrecoverycomposition.ProjectionRequest
}

func (projector *currentCardProjector) ProjectCurrent(_ context.Context, request telegramrecoverycomposition.ProjectionRequest) (telegramflow.CallbackResult, error) {
	projector.requests = append(projector.requests, request)
	return telegramflow.CallbackResult{Card: &telegramflow.CardOutput{
		SessionID: request.Scope.SessionID, MakeActive: true,
		Projection: telegramui.CarrierProjection{Effect: telegramui.EffectEditSameCarrier, Card: telegramui.ProjectedCard{
			Pages:    []telegramui.ContentPage{{Content: "current state", Anchors: []string{"current"}}},
			View:     telegramui.PageView{Page: 1, Pages: 1, Anchor: "current", FollowLatest: true},
			Keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionOptions}}}},
		}},
	}}, nil
}

func TestUnknownCallbackPublicVerticalRecoversOnceAndPollingContinues(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	root := t.TempDir()
	operationsPath := filepath.Join(root, "operations.json")
	registryPath := filepath.Join(root, "registry.json")
	operations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := telegrampipeline.OpenFileCallbackRegistry(registryPath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 1024)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	originalText := "lost-original-token"
	original := telegramflow.CallbackOperation{ID: "status:720", UpdateID: 720, CallbackQueryID: "original-query",
		CallbackDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(originalText))),
		Plan: telegrampipeline.CallbackPlan{OperationID: "status:720", UpdateID: 720, SessionID: domain.SessionID(recoverySessionID),
			Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Action: telegramui.ActionOptions, Effect: telegrampipeline.EffectToggleOptions},
		Phase: telegramflow.CallbackClaimed}
	if err := operations.Create(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	unknown := original
	unknown.Phase = telegramflow.CallbackEffectUnknown
	if changed, err := operations.CompareAndSwap(context.Background(), original.ID, telegramflow.CallbackClaimed, unknown); err != nil || !changed {
		t.Fatalf("make original unknown = %t, %v", changed, err)
	}
	normal := &callbackStub{}
	projector := &currentCardProjector{}
	recoveryExecutor, err := telegramrecoverycomposition.New(normal, operations, projector)
	if err != nil {
		t.Fatal(err)
	}
	transport := &verticalTransport{}
	uiState := telegramstate.NewMemoryStore()
	handler, sender, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: registry, UIState: uiState, Messages: messageStub{}, Callbacks: recoveryExecutor, Operations: operations, Sender: transport})
	if err != nil {
		t.Fatal(err)
	}
	if err := recoveryExecutor.Bind(sender); err != nil {
		t.Fatal(err)
	}
	checkpoint := &verticalStore{}
	source := &verticalSource{transport: transport, original: coordinator.Update{ID: 720, Kind: coordinator.UpdateCallback, ActorID: 7,
		ConversationID: 42, ConversationKind: "private", Text: originalText, CallbackQueryID: "original-query", SourceMessageID: 99}}
	loop, err := coordinator.NewLoop(source, checkpoint, handler, sender, readyStub{})
	if err != nil {
		t.Fatal(err)
	}
	if err := loop.Run(context.Background()); !errors.Is(err, errVerticalComplete) {
		t.Fatalf("Run() error = %v", err)
	}
	stored, found, err := operations.Load(context.Background(), original.ID)
	if err != nil || !found || stored.Phase != telegramflow.CallbackEffectResolved || normal.calls != 0 {
		t.Fatalf("original recovery = %#v, effects=%d, %t, %v", stored, normal.calls, found, err)
	}
	checkpointValue, found, err := checkpoint.Load(context.Background())
	if err != nil || !found || checkpointValue.Checkpoint.NextUpdateID != 723 {
		t.Fatalf("polling checkpoint = %#v, %t, %v", checkpointValue, found, err)
	}
	if len(projector.requests) != 1 || projector.requests[0].OperationID != original.ID || projector.requests[0].Scope.SessionID != original.Plan.SessionID {
		t.Fatalf("current projection requests = %#v", projector.requests)
	}
	state, err := uiState.Load(context.Background())
	card, cardFound := state.Card(original.Plan.SessionID)
	if err != nil || !cardFound || card.Carrier != (telegramstate.Carrier{ChatID: 42, MessageID: 500}) || !reflect.DeepEqual(card.History, []string{"current state"}) {
		t.Fatalf("reprojected card = %#v, %t, %v", card, cardFound, err)
	}
	reopenedOperations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	reopenedOriginal, found, err := reopenedOperations.Load(context.Background(), original.ID)
	if err != nil || !found || reopenedOriginal.Phase != telegramflow.CallbackEffectResolved {
		t.Fatalf("reopened original = %#v, %t, %v", reopenedOriginal, found, err)
	}
	reopenedRegistry, err := telegrampipeline.OpenFileCallbackRegistry(registryPath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopenedRegistry.Snapshot(context.Background())
	if err != nil || len(snapshot.Presentations) != 1 {
		t.Fatalf("reopened registry = %#v, %v", snapshot, err)
	}
}

func TestSignedStatusRecoverySurvivesReopenAndRejectsSecondClick(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	root := t.TempDir()
	operationsPath := filepath.Join(root, "operations.json")
	registryPath := filepath.Join(root, "registry.json")
	operations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := telegrampipeline.OpenFileCallbackRegistry(registryPath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x35}, 1024)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	binding := telegramflow.StatusRecoveryBinding{OperationID: "status:731", UpdateID: 731,
		Scope: telegramflow.RecoveryScope{Kind: telegramflow.RecoveryScopeGlobal}, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99},
		Sequence: 731, Prepared: true, Edit: true}
	prepared, err := telegramrecoverycomposition.PrepareStatusRecovery(binding.OperationID, 42, binding, presenter)
	if err != nil {
		t.Fatal(err)
	}
	operation := telegramflow.StatusOperation{ID: binding.OperationID, Sequence: binding.Sequence, Status: prepared.Status, Keyboard: prepared.Keyboard,
		Prepared: &prepared, Edit: true, Phase: telegramflow.StatusQueued, Recovery: &binding}
	persisted, _, err := operations.EnqueueStatus(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	unknown := persisted
	unknown.Phase = telegramflow.StatusSendUnknown
	if changed, err := operations.CompareAndSwapStatus(context.Background(), operation.ID, telegramflow.StatusQueued, unknown); err != nil || !changed {
		t.Fatalf("make status unknown = %t, %v", changed, err)
	}
	if err := telegrampipeline.BindPresentation(context.Background(), registry, binding.Carrier, prepared.Presentation); err != nil {
		t.Fatal(err)
	}
	registry, err = telegrampipeline.OpenFileCallbackRegistry(registryPath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	normal := &callbackStub{}
	projector := &projectorStub{}
	executor, err := telegramrecoverycomposition.New(normal, operations, projector)
	if err != nil {
		t.Fatal(err)
	}
	transport := &transportStub{receipt: coordinator.Receipt{MessageID: 99}}
	handler, sender, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: registry, UIState: telegramstate.NewMemoryStore(), Messages: messageStub{}, Callbacks: executor, Operations: operations, Sender: transport})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Bind(sender); err != nil {
		t.Fatal(err)
	}
	callback := coordinator.Update{ID: 900, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: (*prepared.Keyboard)[0][0].CallbackData, CallbackQueryID: "status-recovery", SourceMessageID: 99}
	decision, err := handler.Handle(context.Background(), callback)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.EditStatusWithKeyboard(context.Background(), "status:900", decision.Status, decision.Keyboard); err != nil {
		t.Fatal(err)
	}
	resolved, found, err := operations.LoadStatus(context.Background(), binding.OperationID)
	if err != nil || !found || resolved.Phase != telegramflow.StatusCommitted || transport.edits != 1 || len(projector.requests) != 1 {
		t.Fatalf("resolved status = %#v, edits=%d projections=%d, %t, %v", resolved, transport.edits, len(projector.requests), found, err)
	}
	replay := callback
	replay.ID++
	replay.CallbackQueryID = "status-recovery-replay"
	replayed, err := handler.Handle(context.Background(), replay)
	if err != nil || replayed.Kind != coordinator.DecisionStatus || replayed.Status.CallbackQueryID != replay.CallbackQueryID || len(projector.requests) != 1 {
		t.Fatalf("replayed status recovery = %#v, %v, projections=%d", replayed, err, len(projector.requests))
	}
	reopenedOperations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	reopened, found, err := reopenedOperations.LoadStatus(context.Background(), binding.OperationID)
	if err != nil || !found || reopened.Phase != telegramflow.StatusCommitted || reopened.Recovery == nil || *reopened.Recovery != binding {
		t.Fatalf("reopened status = %#v, %t, %v", reopened, found, err)
	}
	if _, err := telegrampipeline.OpenFileCallbackRegistry(registryPath, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
}
