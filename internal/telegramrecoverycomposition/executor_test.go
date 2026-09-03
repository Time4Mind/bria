package telegramrecoverycomposition_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramrecovery/statusrecovery"
	"bria/internal/telegramrecoverycomposition"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

const recoverySessionID = "123e4567-e89b-12d3-a456-426614174000"

type callbackStub struct {
	calls int
	err   error
}

func (stub *callbackStub) HandleCallback(_ context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	stub.calls++
	if stub.err != nil {
		return telegramflow.CallbackResult{}, stub.err
	}
	return telegramflow.CallbackResult{OperationID: plan.OperationID, Terminal: &telegramflow.TerminalOutput{Text: "normal"}}, nil
}

type messageStub struct{}

func (messageStub) Handle(context.Context, coordinator.Update) (coordinator.Decision, error) {
	return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
}

type transportStub struct {
	receipt coordinator.Receipt
	err     error
	sends   int
	edits   int
}

func (stub *transportStub) SendStatus(context.Context, string, coordinator.Status) (coordinator.Receipt, error) {
	stub.sends++
	return stub.receipt, stub.err
}
func (stub *transportStub) SendStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	stub.sends++
	return stub.receipt, stub.err
}
func (stub *transportStub) EditStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	stub.edits++
	return stub.receipt, stub.err
}

type projectorStub struct {
	requests []telegramrecoverycomposition.ProjectionRequest
}

func (stub *projectorStub) ProjectCurrent(_ context.Context, request telegramrecoverycomposition.ProjectionRequest) (telegramflow.CallbackResult, error) {
	stub.requests = append(stub.requests, request)
	return telegramflow.CallbackResult{Surface: &telegramflow.SurfaceOutput{Text: "current", Keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionMenuStatus}}}}}}, nil
}

func TestStatusRecoveryConfirmRetryCancelAndLostResponse(t *testing.T) {
	for _, test := range []struct {
		name      string
		action    telegramui.Action
		effect    telegrampipeline.CallbackEffect
		transport error
		wantPhase telegramflow.StatusOperationPhase
		wantEdits int
		wantErr   error
	}{
		{"assume delivered", telegramui.ActionStatusRecoveryAssumeDelivered, telegrampipeline.EffectStatusRecoveryAssumeDelivered, nil, telegramflow.StatusCommitted, 0, nil},
		{"retry once", telegramui.ActionStatusRecoveryRetryPossibleDuplicate, telegrampipeline.EffectStatusRecoveryRetryPossibleDuplicate, nil, telegramflow.StatusCommitted, 1, nil},
		{"cancel", telegramui.ActionStatusRecoveryCancel, telegrampipeline.EffectStatusRecoveryCancel, nil, telegramflow.StatusSendUnknown, 0, nil},
		{"retry lost response", telegramui.ActionStatusRecoveryRetryPossibleDuplicate, telegrampipeline.EffectStatusRecoveryRetryPossibleDuplicate, errors.New("timeout after write"), telegramflow.StatusSendUnknown, 1, telegrampipeline.ErrUnknownOperation},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor, operations, transport, projector, binding := recoveryHarness(t, test.transport)
			result, err := executor.HandleCallback(context.Background(), telegrampipeline.CallbackPlan{
				OperationID: "status:900", UpdateID: 900, SessionID: telegramui.GlobalSurfaceID,
				Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 500}, Action: test.action, Effect: test.effect,
				StatusRecovery: &telegrampipeline.CallbackStatusRecoveryPlan{Binding: binding, Decision: test.action},
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("HandleCallback() error = %v, want %v", err, test.wantErr)
			}
			stored, found, loadErr := operations.LoadStatus(context.Background(), binding.OperationID)
			if loadErr != nil || !found || stored.Phase != test.wantPhase || transport.edits != test.wantEdits {
				t.Fatalf("status/edits = %#v/%d, %t, %v", stored, transport.edits, found, loadErr)
			}
			if test.wantErr == nil {
				if result.OperationID != "status:900" || len(projector.requests) != 1 || projector.requests[0] != (telegramrecoverycomposition.ProjectionRequest{
					Scope: binding.Scope, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 500}, OperationID: binding.OperationID, Sequence: binding.Sequence,
				}) {
					t.Fatalf("result/projection = %#v/%#v", result, projector.requests)
				}
			} else if len(projector.requests) != 0 {
				t.Fatalf("lost retry projected before resolution: %#v", projector.requests)
			}
		})
	}
}

func TestStatusRecoveryRejectsTamperBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*telegramflow.StatusRecoveryBinding)
	}{
		{"operation", func(binding *telegramflow.StatusRecoveryBinding) { binding.OperationID = "status:732" }},
		{"update", func(binding *telegramflow.StatusRecoveryBinding) { binding.UpdateID++ }},
		{"scope", func(binding *telegramflow.StatusRecoveryBinding) {
			binding.Scope = statusrecovery.Scope{Kind: statusrecovery.ScopeGlobal}
		}},
		{"session", func(binding *telegramflow.StatusRecoveryBinding) {
			binding.Scope.SessionID = "123e4567-e89b-12d3-a456-426614174001"
		}},
		{"carrier", func(binding *telegramflow.StatusRecoveryBinding) { binding.Carrier.MessageID++ }},
		{"sequence", func(binding *telegramflow.StatusRecoveryBinding) { binding.Sequence++ }},
		{"prepared", func(binding *telegramflow.StatusRecoveryBinding) { binding.Prepared = !binding.Prepared }},
		{"edit", func(binding *telegramflow.StatusRecoveryBinding) { binding.Edit = !binding.Edit }},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor, operations, transport, projector, binding := recoveryHarness(t, nil)
			tampered := binding
			test.mutate(&tampered)
			_, err := executor.HandleCallback(context.Background(), telegrampipeline.CallbackPlan{
				OperationID: "status:901", UpdateID: 901, SessionID: telegramui.GlobalSurfaceID,
				Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 500}, Action: telegramui.ActionStatusRecoveryAssumeDelivered,
				Effect:         telegrampipeline.EffectStatusRecoveryAssumeDelivered,
				StatusRecovery: &telegrampipeline.CallbackStatusRecoveryPlan{Binding: tampered, Decision: telegramui.ActionStatusRecoveryAssumeDelivered},
			})
			stored, _, _ := operations.LoadStatus(context.Background(), binding.OperationID)
			if err == nil || stored.Phase != telegramflow.StatusSendUnknown || transport.edits != 0 || len(projector.requests) != 0 {
				t.Fatalf("tamper result = %v, %#v, edits=%d, projections=%d", err, stored, transport.edits, len(projector.requests))
			}
		})
	}
}

func TestCallbackRecoveryConfirmAndRetryExactEffectOrSend(t *testing.T) {
	for _, test := range []struct {
		name        string
		phase       telegramflow.CallbackOperationPhase
		action      telegramui.Action
		effect      telegrampipeline.CallbackEffect
		wantPhase   telegramflow.CallbackOperationPhase
		wantEffects int
		wantEdits   int
	}{
		{"assume effect applied", telegramflow.CallbackEffectUnknown, telegramui.ActionCallbackEffectConfirmed, telegrampipeline.EffectCallbackEffectConfirmed, telegramflow.CallbackEffectResolved, 0, 0},
		{"retry effect once", telegramflow.CallbackEffectUnknown, telegramui.ActionCallbackEffectRetryPossibleDuplicate, telegrampipeline.EffectCallbackEffectRetryPossibleDuplicate, telegramflow.CallbackEffectResolved, 1, 0},
		{"assume send delivered", telegramflow.CallbackSendUnknown, telegramui.ActionCallbackSendConfirmed, telegrampipeline.EffectCallbackSendConfirmed, telegramflow.CallbackCommitted, 0, 0},
		{"retry send once", telegramflow.CallbackSendUnknown, telegramui.ActionCallbackSendRetryPossibleDuplicate, telegrampipeline.EffectCallbackSendRetryPossibleDuplicate, telegramflow.CallbackCommitted, 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor, operations, transport, projector, normal, binding := callbackRecoveryHarness(t, test.phase)
			result, err := executor.HandleCallback(context.Background(), telegrampipeline.CallbackPlan{
				OperationID: "status:910", UpdateID: 910, SessionID: telegramui.GlobalSurfaceID,
				Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 500}, Action: test.action, Effect: test.effect,
				Recovery: &telegrampipeline.CallbackRecoveryPlan{OperationID: binding.OperationID, UpdateID: binding.UpdateID,
					SessionID: binding.SessionID, Carrier: binding.Carrier, Phase: binding.Phase, Decision: test.action},
			})
			if err != nil {
				t.Fatal(err)
			}
			stored, found, loadErr := operations.Load(context.Background(), binding.OperationID)
			if loadErr != nil || !found || stored.Phase != test.wantPhase || normal.calls != test.wantEffects || transport.edits != test.wantEdits {
				t.Fatalf("callback/effects/edits = %#v/%d/%d, %t, %v", stored, normal.calls, transport.edits, found, loadErr)
			}
			wantScope := statusrecovery.Scope{Kind: statusrecovery.ScopeSession, SessionID: binding.SessionID}
			if result.OperationID != "status:910" || len(projector.requests) != 1 || projector.requests[0] != (telegramrecoverycomposition.ProjectionRequest{
				Scope: wantScope, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 500}, OperationID: binding.OperationID, Sequence: uint64(binding.UpdateID),
			}) {
				t.Fatalf("result/projection = %#v/%#v", result, projector.requests)
			}
		})
	}
}

func TestCallbackRecoveryRejectsExactIdentityTamperBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*telegrampipeline.CallbackRecoveryBinding)
	}{
		{"operation", func(binding *telegrampipeline.CallbackRecoveryBinding) { binding.OperationID = "status:719" }},
		{"update", func(binding *telegrampipeline.CallbackRecoveryBinding) { binding.UpdateID++ }},
		{"session", func(binding *telegrampipeline.CallbackRecoveryBinding) {
			binding.SessionID = "123e4567-e89b-12d3-a456-426614174001"
		}},
		{"carrier", func(binding *telegrampipeline.CallbackRecoveryBinding) { binding.Carrier.MessageID++ }},
		{"phase", func(binding *telegrampipeline.CallbackRecoveryBinding) {
			binding.Phase = telegrampipeline.CallbackSendUnknownPhase
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor, operations, transport, projector, normal, binding := callbackRecoveryHarness(t, telegramflow.CallbackEffectUnknown)
			test.mutate(&binding)
			_, err := executor.HandleCallback(context.Background(), telegrampipeline.CallbackPlan{
				OperationID: "status:911", UpdateID: 911, SessionID: telegramui.GlobalSurfaceID,
				Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 500}, Action: telegramui.ActionCallbackEffectConfirmed,
				Effect: telegrampipeline.EffectCallbackEffectConfirmed, Recovery: &telegrampipeline.CallbackRecoveryPlan{OperationID: binding.OperationID,
					UpdateID: binding.UpdateID, SessionID: binding.SessionID, Carrier: binding.Carrier, Phase: binding.Phase, Decision: telegramui.ActionCallbackEffectConfirmed},
			})
			stored, _, _ := operations.Load(context.Background(), "status:720")
			if err == nil || stored.Phase != telegramflow.CallbackEffectUnknown || normal.calls != 0 || transport.edits != 0 || len(projector.requests) != 0 {
				t.Fatalf("tamper result = %v, %#v, effects=%d, edits=%d, projections=%d", err, stored, normal.calls, transport.edits, len(projector.requests))
			}
		})
	}
}

func TestCallbackEffectRetryLostResponseStaysUnknownAndDoesNotProject(t *testing.T) {
	executor, operations, _, projector, normal, binding := callbackRecoveryHarness(t, telegramflow.CallbackEffectUnknown)
	normal.err = errors.New("timeout after side effect")
	_, err := executor.HandleCallback(context.Background(), telegrampipeline.CallbackPlan{
		OperationID: "status:912", UpdateID: 912, SessionID: telegramui.GlobalSurfaceID,
		Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 500}, Action: telegramui.ActionCallbackEffectRetryPossibleDuplicate,
		Effect: telegrampipeline.EffectCallbackEffectRetryPossibleDuplicate, Recovery: &telegrampipeline.CallbackRecoveryPlan{OperationID: binding.OperationID,
			UpdateID: binding.UpdateID, SessionID: binding.SessionID, Carrier: binding.Carrier, Phase: binding.Phase, Decision: telegramui.ActionCallbackEffectRetryPossibleDuplicate},
	})
	stored, _, _ := operations.Load(context.Background(), binding.OperationID)
	if !errors.Is(err, telegrampipeline.ErrUnknownOperation) || stored.Phase != telegramflow.CallbackEffectRetryUnknown || normal.calls != 1 || len(projector.requests) != 0 {
		t.Fatalf("lost retry = %v, %#v, effects=%d, projections=%d", err, stored, normal.calls, len(projector.requests))
	}
	_, secondErr := executor.HandleCallback(context.Background(), telegrampipeline.CallbackPlan{
		OperationID: "status:912", UpdateID: 912, SessionID: telegramui.GlobalSurfaceID,
		Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 500}, Action: telegramui.ActionCallbackEffectRetryPossibleDuplicate,
		Effect: telegrampipeline.EffectCallbackEffectRetryPossibleDuplicate, Recovery: &telegrampipeline.CallbackRecoveryPlan{OperationID: binding.OperationID,
			UpdateID: binding.UpdateID, SessionID: binding.SessionID, Carrier: binding.Carrier, Phase: binding.Phase, Decision: telegramui.ActionCallbackEffectRetryPossibleDuplicate},
	})
	if secondErr == nil || normal.calls != 1 {
		t.Fatalf("second fenced retry = %v, effects=%d", secondErr, normal.calls)
	}
}

func callbackRecoveryHarness(t *testing.T, phase telegramflow.CallbackOperationPhase) (*telegramrecoverycomposition.Executor, *telegramflow.MemoryCallbackOperationStore, *transportStub, *projectorStub, *callbackStub, telegrampipeline.CallbackRecoveryBinding) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 512)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	operations := telegramflow.NewMemoryCallbackOperationStore()
	plan := telegrampipeline.CallbackPlan{OperationID: "status:720", UpdateID: 720, SessionID: recoverySessionID,
		Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Action: telegramui.ActionOptions, Effect: telegrampipeline.EffectToggleOptions}
	operation := telegramflow.CallbackOperation{ID: plan.OperationID, UpdateID: plan.UpdateID, CallbackQueryID: "original-query",
		CallbackDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Plan: plan, Phase: telegramflow.CallbackClaimed}
	if err := operations.Create(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	unknown := operation
	unknown.Phase = telegramflow.CallbackEffectUnknown
	if changed, err := operations.CompareAndSwap(context.Background(), operation.ID, telegramflow.CallbackClaimed, unknown); err != nil || !changed {
		t.Fatalf("make callback unknown = %t, %v", changed, err)
	}
	normal := &callbackStub{}
	projector := &projectorStub{}
	executor, err := telegramrecoverycomposition.New(normal, operations, projector)
	if err != nil {
		t.Fatal(err)
	}
	transport := &transportStub{receipt: coordinator.Receipt{MessageID: 99}}
	_, sender, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }), UIState: telegramstate.NewMemoryStore(), Messages: messageStub{},
		Callbacks: executor, Operations: operations, Sender: transport})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Bind(sender); err != nil {
		t.Fatal(err)
	}
	if phase == telegramflow.CallbackSendUnknown {
		empty := coordinator.KeyboardMarkup{}
		prepared := telegramflow.Prepared{OperationID: operation.ID,
			Status:   coordinator.Status{ConversationID: 42, Text: "normal", CallbackQueryID: operation.CallbackQueryID, SourceMessageID: 99},
			Keyboard: &empty, Edit: true, Terminal: true}
		ready := unknown
		ready.Phase = telegramflow.CallbackPrepared
		ready.Prepared = &prepared
		if changed, readyErr := operations.CompareAndSwap(context.Background(), operation.ID, telegramflow.CallbackEffectUnknown, ready); readyErr != nil || !changed {
			t.Fatalf("prepare callback send = %t, %v", changed, readyErr)
		}
		transport.err = errors.New("timeout after write")
		if _, sendErr := sender.EditStatusWithKeyboard(context.Background(), operation.ID, prepared.Status, prepared.Keyboard); sendErr == nil {
			t.Fatal("callback send did not become unknown")
		}
		transport.err = nil
	}
	normal.calls = 0
	transport.edits = 0
	binding := telegrampipeline.CallbackRecoveryBinding{OperationID: operation.ID, UpdateID: operation.UpdateID,
		SessionID: plan.SessionID, Carrier: plan.Carrier, Phase: string(phase)}
	return executor, operations, transport, projector, normal, binding
}

func recoveryHarness(t *testing.T, transportErr error) (*telegramrecoverycomposition.Executor, *telegramflow.MemoryCallbackOperationStore, *transportStub, *projectorStub, telegramflow.StatusRecoveryBinding) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 512)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	operations := telegramflow.NewMemoryCallbackOperationStore()
	binding := telegramflow.StatusRecoveryBinding{OperationID: "status:731", UpdateID: 731,
		Scope:   statusrecovery.Scope{Kind: statusrecovery.ScopeSession, SessionID: recoverySessionID},
		Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Sequence: 731, Edit: true}
	status := telegramflow.StatusOperation{ID: binding.OperationID, Sequence: binding.Sequence,
		Status: coordinator.Status{ConversationID: 42, Text: "recover", SourceMessageID: 99}, Edit: true, Phase: telegramflow.StatusQueued, Recovery: &binding}
	if _, _, err := operations.EnqueueStatus(context.Background(), status); err != nil {
		t.Fatal(err)
	}
	unknown := status
	unknown.Phase = telegramflow.StatusSendUnknown
	if changed, err := operations.CompareAndSwapStatus(context.Background(), status.ID, telegramflow.StatusQueued, unknown); err != nil || !changed {
		t.Fatalf("make status unknown = %t, %v", changed, err)
	}
	normal := &callbackStub{}
	transport := &transportStub{receipt: coordinator.Receipt{MessageID: 99}, err: transportErr}
	_, sender, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }), UIState: telegramstate.NewMemoryStore(), Messages: messageStub{},
		Callbacks: normal, Operations: operations, Sender: transport})
	if err != nil {
		t.Fatal(err)
	}
	projector := &projectorStub{}
	executor, err := telegramrecoverycomposition.New(normal, operations, projector)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Bind(sender); err != nil {
		t.Fatal(err)
	}
	return executor, operations, transport, projector, binding
}
