package interactionflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/interactionflow"
	"bria/internal/runtimeprotocol"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

const testSessionID = domain.SessionID("00000000-0000-4000-8000-000000000001")

func TestFlowPersistsBeforeSendAndReturnsOneExactlyCorrelatedQuestionResponse(t *testing.T) {
	t.Parallel()

	store := interactionflow.NewMemoryStore()
	sender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 91}}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	flow := mustFlow(t, store, sender, now, time.Minute)
	envelope := questionEnvelope()
	responseCh := make(chan sessionruntime.InteractionResponse, 1)
	errorCh := make(chan error, 1)
	go func() {
		response, err := flow.ResolveInteraction(context.Background(), envelope)
		responseCh <- response
		errorCh <- err
	}()
	delivery := sender.wait(t)
	if delivery.SessionID != envelope.SessionID || delivery.MessageID != envelope.MessageID || delivery.ProviderRequestID != envelope.Request.ID {
		t.Fatalf("delivery correlation = %#v", delivery)
	}
	if delivery.Surface.Keyboard.Rows[1][0].Target.InteractionChoice != 2 {
		t.Fatalf("question keyboard = %#v", delivery.Surface.Keyboard)
	}

	result, err := flow.HandleCallback(context.Background(), interactionPlan(delivery.OperationID, "telegram-callback:11", telegramui.ActionInteractionChoice, 2))
	if err != nil {
		t.Fatalf("HandleCallback() error = %v", err)
	}
	if result.OperationID != "telegram-callback:11" || result.Terminal == nil {
		t.Fatalf("callback result = %#v, want terminal acknowledgement", result)
	}
	select {
	case response := <-responseCh:
		want := sessionruntime.InteractionResponse{ID: envelope.Request.ID, Outcome: runtimeprotocol.OutcomeAnswered, Answers: map[string][]string{"choice": {"B"}}}
		if !reflect.DeepEqual(response, want) {
			t.Fatalf("response = %#v, want %#v", response, want)
		}
	case <-time.After(time.Second):
		t.Fatal("ResolveInteraction did not receive callback response")
	}
	if err := <-errorCh; err != nil {
		t.Fatalf("ResolveInteraction error = %v", err)
	}
	operation, found, err := store.Load(context.Background(), delivery.OperationID)
	if err != nil || !found || operation.Phase != interactionflow.PhaseProviderResponseUnknown {
		t.Fatalf("durable terminal operation = (%#v, %t, %v)", operation, found, err)
	}
	if operation.SessionID != envelope.SessionID || operation.MessageID != envelope.MessageID || operation.ProviderRequestID != envelope.Request.ID {
		t.Fatalf("stored exact correlation = %#v", operation)
	}
}

func TestFlowProgressesMultipleQuestionsAndRecoversSameCallbackWithoutDoubleAdvance(t *testing.T) {
	t.Parallel()

	store := interactionflow.NewMemoryStore()
	sender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 91}}
	flow := mustFlow(t, store, sender, time.Now().UTC(), time.Minute)
	envelope := questionEnvelope()
	envelope.Request.Questions = append(envelope.Request.Questions, runtimeprotocol.Question{
		ID: "second", Header: "Second", Text: "Pick again", Options: []runtimeprotocol.Option{{Label: "X"}, {Label: "Y"}},
	})
	done := make(chan sessionruntime.InteractionResponse, 1)
	go func() {
		response, _ := flow.ResolveInteraction(context.Background(), envelope)
		done <- response
	}()
	delivery := sender.wait(t)
	firstPlan := interactionPlan(delivery.OperationID, "telegram-callback:21", telegramui.ActionInteractionChoice, 1)
	first, err := flow.HandleCallback(context.Background(), firstPlan)
	if err != nil || first.Surface == nil || first.Terminal != nil {
		t.Fatalf("first callback = %#v, err=%v", first, err)
	}
	recovered, err := flow.HandleCallback(context.Background(), firstPlan)
	if err != nil || !reflect.DeepEqual(recovered, first) {
		t.Fatalf("recovered callback = %#v, err=%v want %#v", recovered, err, first)
	}
	second, err := flow.HandleCallback(context.Background(), interactionPlan(delivery.OperationID, "telegram-callback:22", telegramui.ActionInteractionChoice, 2))
	if err != nil || second.Terminal == nil {
		t.Fatalf("second callback = %#v, err=%v", second, err)
	}
	response := <-done
	wantAnswers := map[string][]string{"choice": {"A"}, "second": {"Y"}}
	if !reflect.DeepEqual(response.Answers, wantAnswers) {
		t.Fatalf("answers = %#v, want %#v", response.Answers, wantAnswers)
	}
}

func TestFlowNeverAutomaticallyRepeatsUnknownSendOrProviderResponseAfterReopen(t *testing.T) {
	t.Parallel()

	store := interactionflow.NewMemoryStore()
	unknownSender := &checkingSender{store: store, err: errors.New("ambiguous transport with secret")}
	flow := mustFlow(t, store, unknownSender, time.Now().UTC(), time.Minute)
	envelope := questionEnvelope()
	if _, err := flow.ResolveInteraction(context.Background(), envelope); !errors.Is(err, interactionflow.ErrSendUnknown) {
		t.Fatalf("first ResolveInteraction error = %v, want ErrSendUnknown", err)
	}
	reopenedSender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 92}}
	reopened := mustFlow(t, store, reopenedSender, time.Now().UTC(), time.Minute)
	if _, err := reopened.ResolveInteraction(context.Background(), envelope); !errors.Is(err, interactionflow.ErrSendUnknown) {
		t.Fatalf("reopened send error = %v, want ErrSendUnknown", err)
	}
	if reopenedSender.calls() != 0 {
		t.Fatalf("reopened flow repeated unknown send %d times", reopenedSender.calls())
	}

	terminal := interactionflow.Operation{
		ID: "interaction:terminal", SessionID: envelope.SessionID, MessageID: envelope.MessageID,
		ProviderRequestID: envelope.Request.ID, ConversationID: 42, CarrierMessageID: 91,
		Request: envelope.Request, Phase: interactionflow.PhaseProviderResponseUnknown, QuestionIndex: 1,
		Answers: map[string][]string{"choice": {"A"}}, Response: &runtimeprotocol.InteractionResponse{
			ID: envelope.Request.ID, Outcome: runtimeprotocol.OutcomeAnswered, Answers: map[string][]string{"choice": {"A"}},
		},
		Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if _, err := store.Create(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ResumeOperation(context.Background(), terminal.ID); !errors.Is(err, interactionflow.ErrProviderResponseUnknown) {
		t.Fatalf("ResumeOperation error = %v, want ErrProviderResponseUnknown", err)
	}
}

func TestFlowContextCancellationReturnsExplicitTypedCancellation(t *testing.T) {
	t.Parallel()

	store := interactionflow.NewMemoryStore()
	sender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 91}}
	flow := mustFlow(t, store, sender, time.Now().UTC(), time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan sessionruntime.InteractionResponse, 1)
	go func() {
		response, _ := flow.ResolveInteraction(ctx, questionEnvelope())
		done <- response
	}()
	_ = sender.wait(t)
	cancel()
	select {
	case response := <-done:
		if response.Outcome != runtimeprotocol.OutcomeCancelled || response.ID != "provider-request-1" {
			t.Fatalf("cancel response = %#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock provider interaction")
	}
}

func TestFlowTimeoutIsDurableAndReturnsExplicitTypedCancellation(t *testing.T) {
	t.Parallel()

	store := interactionflow.NewMemoryStore()
	sender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 91}}
	now := time.Now().UTC()
	flow := mustFlow(t, store, sender, now, 20*time.Millisecond)
	response, err := flow.ResolveInteraction(context.Background(), questionEnvelope())
	if err != nil {
		t.Fatalf("ResolveInteraction timeout error = %v", err)
	}
	if response.ID != "provider-request-1" || response.Outcome != runtimeprotocol.OutcomeCancelled {
		t.Fatalf("timeout response = %#v", response)
	}
	delivery := sender.wait(t)
	operation, found, err := store.Load(context.Background(), delivery.OperationID)
	if err != nil || !found || operation.Resolution != "timeout" || operation.Phase != interactionflow.PhaseProviderResponseUnknown {
		t.Fatalf("timeout operation = (%#v, %t, %v)", operation, found, err)
	}
}

func TestFlowMapsSignedApprovalOnlyToAdvertisedDecisionAndRejectsWrongCarrier(t *testing.T) {
	t.Parallel()

	store := interactionflow.NewMemoryStore()
	sender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 91}}
	flow := mustFlow(t, store, sender, time.Now().UTC(), time.Minute)
	envelope := telegramcontroller.InteractionEnvelope{
		SessionID: testSessionID, MessageID: "telegram-update:8",
		Request: runtimeprotocol.InteractionRequest{
			ID: "provider-approval-1", Kind: runtimeprotocol.InteractionCommandApproval,
			ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", ApprovalID: "approval-1",
			Command: "go test ./...", Decisions: []runtimeprotocol.ApprovalDecision{
				runtimeprotocol.DecisionAccept, runtimeprotocol.DecisionDecline, runtimeprotocol.DecisionCancel,
			},
		},
	}
	if _, err := runtimeprotocol.EncodeAdapterLine(runtimeprotocol.AdapterMessage{
		Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeInteractionRequest,
		RequestID: "validation", InteractionRequest: &envelope.Request,
	}, runtimeprotocol.Limits{}); err != nil {
		t.Fatalf("approval fixture is invalid: %v", err)
	}
	done := make(chan sessionruntime.InteractionResponse, 1)
	go func() {
		response, _ := flow.ResolveInteraction(context.Background(), envelope)
		done <- response
	}()
	delivery := sender.wait(t)
	wrong := interactionPlan(delivery.OperationID, "telegram-callback:31", telegramui.ActionInteractionAccept, 0)
	wrong.Carrier.MessageID++
	if _, err := flow.HandleCallback(context.Background(), wrong); !errors.Is(err, interactionflow.ErrStaleCallback) {
		t.Fatalf("wrong carrier error = %v, want ErrStaleCallback", err)
	}
	result, err := flow.HandleCallback(context.Background(), interactionPlan(delivery.OperationID, "telegram-callback:32", telegramui.ActionInteractionAccept, 0))
	if err != nil || result.Terminal == nil {
		t.Fatalf("approval callback = %#v, err=%v", result, err)
	}
	response := <-done
	if response.Decision != runtimeprotocol.DecisionAccept || response.Outcome != runtimeprotocol.OutcomeAnswered {
		t.Fatalf("approval response = %#v", response)
	}
}

func TestFlowRoutesSignedOtherToExactlyNextOwnerTextBeforeNormalInput(t *testing.T) {
	t.Parallel()

	store := interactionflow.NewMemoryStore()
	sender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 91}}
	flow := mustTextFlow(t, store, sender, nil)
	envelope := questionEnvelope()
	envelope.Request.Questions[0].IsOther = true
	done := make(chan sessionruntime.InteractionResponse, 1)
	go func() {
		response, _ := flow.ResolveInteraction(context.Background(), envelope)
		done <- response
	}()
	delivery := sender.wait(t)
	other, err := flow.HandleCallback(context.Background(), interactionPlan(
		delivery.OperationID, "telegram-callback:other", telegramui.ActionInteractionOther, 0,
	))
	if err != nil || other.Surface == nil || other.Terminal != nil {
		t.Fatalf("Other callback = %#v, err=%v", other, err)
	}
	if len(other.Surface.Keyboard.Rows) != 1 || other.Surface.Keyboard.Rows[0][0].Action != telegramui.ActionInteractionCancel {
		t.Fatalf("Other pending keyboard = %#v, want signed cancel only", other.Surface.Keyboard)
	}
	result, err := flow.ResolvePendingText(context.Background(), telegramcontroller.InteractionTextInput{
		ActorID: 7, ConversationID: 42, ConversationKind: "private", SourceMessageID: 101,
		ReplyToMessageID: 91, Text: "a typed free-form answer",
	})
	if err != nil || !result.Handled || result.Secret || !result.DeletionKnown {
		t.Fatalf("ResolvePendingText() = %#v, err=%v", result, err)
	}
	select {
	case response := <-done:
		if !reflect.DeepEqual(response.Answers, map[string][]string{"choice": {"a typed free-form answer"}}) {
			t.Fatalf("provider answer = %#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("provider did not receive free-form answer")
	}
}

func TestFlowDeletesSecretOtherBeforeOneShotProviderHandoffAndNeverPersistsIt(t *testing.T) {
	t.Parallel()

	const secret = "secret-answer-never-persisted"
	store := interactionflow.NewMemoryStore()
	sender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 91}}
	deleter := &checkingSecretDeleter{store: store}
	flow := mustTextFlow(t, store, sender, deleter)
	envelope := questionEnvelope()
	envelope.Request.Questions[0].IsOther = true
	envelope.Request.Questions[0].IsSecret = true
	done := make(chan sessionruntime.InteractionResponse, 1)
	go func() {
		response, _ := flow.ResolveInteraction(context.Background(), envelope)
		done <- response
	}()
	delivery := sender.wait(t)
	if _, err := flow.HandleCallback(context.Background(), interactionPlan(
		delivery.OperationID, "telegram-callback:secret-other", telegramui.ActionInteractionOther, 0,
	)); err != nil {
		t.Fatalf("Other callback error = %v", err)
	}
	result, err := flow.ResolvePendingText(context.Background(), telegramcontroller.InteractionTextInput{
		ActorID: 7, ConversationID: 42, ConversationKind: "private", SourceMessageID: 102, Text: secret,
	})
	if err != nil || !result.Handled || !result.Secret || !result.DeletionKnown {
		t.Fatalf("ResolvePendingText() = %#v, err=%v", result, err)
	}
	if deleter.chatID != 42 || deleter.messageID != 102 {
		t.Fatalf("deleted Telegram carrier = (%d,%d)", deleter.chatID, deleter.messageID)
	}
	select {
	case response := <-done:
		if !reflect.DeepEqual(response.Answers, map[string][]string{"choice": {secret}}) {
			t.Fatalf("provider secret answer was not exact")
		}
	case <-time.After(time.Second):
		t.Fatal("provider did not receive one-shot secret answer")
	}
	operation, found, err := store.Load(context.Background(), delivery.OperationID)
	if err != nil || !found || operation.Phase != interactionflow.PhaseProviderResponseUnknown || !operation.SecretResponse || operation.Response != nil {
		t.Fatalf("secret durable fence = (%#v,%t,%v)", operation, found, err)
	}
	persisted, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), secret) {
		t.Fatal("secret answer reached durable operation")
	}
}

func TestSecretSourceTombstoneSurvivesProviderAckPruneAndRestart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "interactions.json")
	store, err := interactionflow.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	sender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 91}}
	deleter := &checkingSecretDeleter{store: store}
	flow := mustTextFlow(t, store, sender, deleter)
	envelope := questionEnvelope()
	envelope.Request.Questions[0].IsOther = true
	envelope.Request.Questions[0].IsSecret = true
	done := make(chan sessionruntime.InteractionResponse, 1)
	go func() {
		response, _ := flow.ResolveInteraction(context.Background(), envelope)
		done <- response
	}()
	delivery := sender.wait(t)
	if _, err := flow.HandleCallback(context.Background(), interactionPlan(
		delivery.OperationID, "telegram-callback:secret-replay", telegramui.ActionInteractionOther, 0,
	)); err != nil {
		t.Fatal(err)
	}
	input := telegramcontroller.InteractionTextInput{
		ActorID: 7, ConversationID: 42, ConversationKind: "private", SourceMessageID: 103, Text: "one-shot-secret",
	}
	result, err := flow.ResolvePendingText(context.Background(), input)
	if err != nil || !result.Handled || !result.Secret || !result.DeletionKnown {
		t.Fatalf("ResolvePendingText() = %#v, err=%v", result, err)
	}
	<-done
	if err := flow.ConfirmInteractionResponse(context.Background(), interactionflow.ResponseAcceptance{
		SessionID: envelope.SessionID, MessageID: envelope.MessageID, ProviderRequestID: envelope.Request.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Load(context.Background(), delivery.OperationID); err != nil || found {
		t.Fatalf("provider operation survived ack: found=%t err=%v", found, err)
	}

	reopened, err := interactionflow.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := mustTextFlow(t, reopened, &checkingSender{store: reopened}, nil)
	replayed, err := restarted.ConsumeBoundSourceMessage(context.Background(), input)
	if err != nil || !replayed.Handled || !replayed.Secret || !replayed.DeletionKnown || replayed.Status == "" {
		t.Fatalf("ConsumeBoundSourceMessage(replay) = %#v, err=%v", replayed, err)
	}
	for _, mismatch := range []telegramcontroller.InteractionTextInput{
		{ActorID: 8, ConversationID: 42, SourceMessageID: 103},
		{ActorID: 7, ConversationID: 43, SourceMessageID: 103},
		{ActorID: 7, ConversationID: 42, SourceMessageID: 104},
	} {
		got, err := restarted.ConsumeBoundSourceMessage(context.Background(), mismatch)
		if err != nil || got.Handled {
			t.Fatalf("mismatched source was consumed: input=%#v result=%#v err=%v", mismatch, got, err)
		}
	}
}

func TestExactSecretRedeliveryRetriesUnknownDeletionAndCompletesLiveProviderRequest(t *testing.T) {
	t.Parallel()

	store := interactionflow.NewMemoryStore()
	sender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 91}}
	deleter := &checkingSecretDeleter{store: store, err: errors.New("ambiguous delete")}
	flow := mustTextFlow(t, store, sender, deleter)
	envelope := questionEnvelope()
	envelope.Request.Questions[0].IsOther = true
	envelope.Request.Questions[0].IsSecret = true
	done := make(chan sessionruntime.InteractionResponse, 1)
	go func() {
		response, _ := flow.ResolveInteraction(context.Background(), envelope)
		done <- response
	}()
	delivery := sender.wait(t)
	if _, err := flow.HandleCallback(context.Background(), interactionPlan(
		delivery.OperationID, "telegram-callback:secret-delete-retry", telegramui.ActionInteractionOther, 0,
	)); err != nil {
		t.Fatal(err)
	}
	input := telegramcontroller.InteractionTextInput{
		ActorID: 7, ConversationID: 42, ConversationKind: "private", SourceMessageID: 104, Text: "retry-secret",
	}
	if _, err := flow.ResolvePendingText(context.Background(), input); !errors.Is(err, interactionflow.ErrSecretDeletionUnknown) {
		t.Fatalf("first deletion error = %v", err)
	}
	select {
	case <-done:
		t.Fatal("provider request ended while exact secret deletion could still be retried")
	default:
	}
	deleter.err = nil
	result, err := flow.ConsumeBoundSourceMessage(context.Background(), input)
	if err != nil || !result.Handled || !result.Secret || !result.DeletionKnown {
		t.Fatalf("ConsumeBoundSourceMessage() = %#v, err=%v", result, err)
	}
	select {
	case response := <-done:
		if !reflect.DeepEqual(response.Answers, map[string][]string{"choice": {"retry-secret"}}) {
			t.Fatalf("provider retry response = %#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("provider request did not complete after exact deletion retry")
	}
}

func TestSecretRedeliveryRepairsCrashGapBetweenOperationFenceAndSourceTombstone(t *testing.T) {
	t.Parallel()

	base := interactionflow.NewMemoryStore()
	store := &failRecordSourceOnce{Store: base, fail: true}
	sender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 91}}
	deleter := &checkingSecretDeleter{store: store}
	flow := mustTextFlow(t, store, sender, deleter)
	envelope := questionEnvelope()
	envelope.Request.Questions[0].IsOther = true
	envelope.Request.Questions[0].IsSecret = true
	done := make(chan struct{}, 1)
	go func() {
		_, _ = flow.ResolveInteraction(context.Background(), envelope)
		done <- struct{}{}
	}()
	delivery := sender.wait(t)
	if _, err := flow.HandleCallback(context.Background(), interactionPlan(
		delivery.OperationID, "telegram-callback:split-gap", telegramui.ActionInteractionOther, 0,
	)); err != nil {
		t.Fatal(err)
	}
	input := telegramcontroller.InteractionTextInput{
		ActorID: 7, ConversationID: 42, ConversationKind: "private", SourceMessageID: 105, Text: "gap-secret",
	}
	if _, err := flow.ResolvePendingText(context.Background(), input); !errors.Is(err, errInjectedSourceWrite) {
		t.Fatalf("fault injection error = %v", err)
	}
	<-done

	// A fresh Flow models reopen. Tombstone-only routing misses, then the exact
	// fenced pending operation repairs the content-free tombstone and consumes
	// the same source without allowing normal turn routing.
	restarted := mustTextFlow(t, store, &checkingSender{store: store}, deleter)
	if result, err := restarted.ConsumeBoundSourceMessage(context.Background(), input); err != nil || result.Handled {
		t.Fatalf("pre-repair tombstone lookup = %#v, err=%v", result, err)
	}
	result, err := restarted.ResolvePendingText(context.Background(), input)
	if err != nil || !result.Handled || !result.Secret || !result.DeletionKnown {
		t.Fatalf("repaired ResolvePendingText() = %#v, err=%v", result, err)
	}
	operation, found, err := store.Load(context.Background(), delivery.OperationID)
	if err != nil || !found || operation.Phase != interactionflow.PhaseProviderResponseUnknown {
		t.Fatalf("repaired operation = (%#v,%t,%v)", operation, found, err)
	}
}

func TestFlowRejectsOtherWhenProviderDidNotAdvertiseIt(t *testing.T) {
	t.Parallel()

	store := interactionflow.NewMemoryStore()
	sender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 91}}
	flow := mustTextFlow(t, store, sender, nil)
	go func() { _, _ = flow.ResolveInteraction(context.Background(), questionEnvelope()) }()
	delivery := sender.wait(t)
	if _, err := flow.HandleCallback(context.Background(), interactionPlan(
		delivery.OperationID, "telegram-callback:unadvertised-other", telegramui.ActionInteractionOther, 0,
	)); !errors.Is(err, interactionflow.ErrInvalidCallback) {
		t.Fatalf("unadvertised Other error = %v", err)
	}
}

func TestFlowPrunesOnlyAfterExactProviderResponseAcceptance(t *testing.T) {
	t.Parallel()

	store := interactionflow.NewMemoryStore()
	sender := &checkingSender{store: store, receipt: interactionflow.DeliveryReceipt{CarrierMessageID: 91}}
	flow := mustTextFlow(t, store, sender, nil)
	envelope := questionEnvelope()
	done := make(chan sessionruntime.InteractionResponse, 1)
	go func() {
		response, _ := flow.ResolveInteraction(context.Background(), envelope)
		done <- response
	}()
	delivery := sender.wait(t)
	if _, err := flow.HandleCallback(context.Background(), interactionPlan(delivery.OperationID, "telegram-callback:confirm", telegramui.ActionInteractionChoice, 1)); err != nil {
		t.Fatal(err)
	}
	<-done
	if _, found, err := store.Load(context.Background(), delivery.OperationID); err != nil || !found {
		t.Fatalf("operation disappeared before provider ack: found=%t err=%v", found, err)
	}
	wrong := interactionflow.ResponseAcceptance{SessionID: envelope.SessionID, MessageID: "wrong", ProviderRequestID: envelope.Request.ID}
	if err := flow.ConfirmInteractionResponse(context.Background(), wrong); !errors.Is(err, interactionflow.ErrInvalidEnvelope) {
		t.Fatalf("wrong acceptance error = %v", err)
	}
	if err := flow.ConfirmInteractionResponse(context.Background(), interactionflow.ResponseAcceptance{
		SessionID: envelope.SessionID, MessageID: envelope.MessageID, ProviderRequestID: envelope.Request.ID,
	}); err != nil {
		t.Fatalf("ConfirmInteractionResponse() error = %v", err)
	}
	if _, found, err := store.Load(context.Background(), delivery.OperationID); err != nil || found {
		t.Fatalf("confirmed operation was not pruned: found=%t err=%v", found, err)
	}
}

func mustFlow(t *testing.T, store interactionflow.Store, sender interactionflow.DeliverySender, now time.Time, timeout time.Duration) *interactionflow.Flow {
	t.Helper()
	flow, err := interactionflow.New(store, sender, interactionflow.Options{
		ConversationID: 42, Timeout: timeout, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	var _ telegramcontroller.InteractionHandler = flow
	var _ telegramflow.CallbackExecutor = flow
	return flow
}

func mustTextFlow(t *testing.T, store interactionflow.Store, sender interactionflow.DeliverySender, deleter interactionflow.SecretMessageDeleter) *interactionflow.Flow {
	t.Helper()
	flow, err := interactionflow.New(store, sender, interactionflow.Options{
		ConversationID: 42, OwnerActorID: 7, SecretDeleter: deleter,
		Timeout: time.Minute, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	var _ telegramcontroller.InteractionTextHandler = flow
	return flow
}

func questionEnvelope() telegramcontroller.InteractionEnvelope {
	return telegramcontroller.InteractionEnvelope{
		SessionID: testSessionID, MessageID: "telegram-update:7",
		Request: runtimeprotocol.InteractionRequest{
			ID: "provider-request-1", Kind: runtimeprotocol.InteractionQuestion,
			ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Blocking: true,
			Questions: []runtimeprotocol.Question{{
				ID: "choice", Header: "Choose", Text: "Pick one",
				Options: []runtimeprotocol.Option{{Label: "A"}, {Label: "B"}},
			}},
		},
	}
}

func interactionPlan(requestID, operationID string, action telegramui.Action, choice int) telegrampipeline.CallbackPlan {
	return telegrampipeline.CallbackPlan{
		OperationID: operationID, UpdateID: 11, SessionID: testSessionID,
		Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 91}, Action: action,
		Target:      telegramui.ButtonTarget{InteractionChoice: choice},
		Interaction: &telegrampipeline.CallbackInteraction{RequestID: requestID, ChoiceIndex: choice},
	}
}

type checkingSender struct {
	mu      sync.Mutex
	store   interactionflow.Store
	receipt interactionflow.DeliveryReceipt
	err     error
	sent    []interactionflow.Delivery
	wake    chan struct{}
}

type checkingSecretDeleter struct {
	store     interactionflow.Store
	chatID    int64
	messageID int64
	err       error
}

var errInjectedSourceWrite = errors.New("injected source tombstone write failure")

type failRecordSourceOnce struct {
	interactionflow.Store
	fail bool
}

func (store *failRecordSourceOnce) RecordConsumedSource(ctx context.Context, source interactionflow.ConsumedSource) (interactionflow.ConsumedSource, error) {
	if store.fail {
		store.fail = false
		return interactionflow.ConsumedSource{}, errInjectedSourceWrite
	}
	return store.Store.RecordConsumedSource(ctx, source)
}

func (deleter *checkingSecretDeleter) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	operation, found, err := deleter.store.PendingText(ctx, chatID)
	if err != nil || !found || operation.Phase != interactionflow.PhaseSecretDeletionUnknown || operation.SecretSourceMessageID != messageID {
		return errors.New("secret deletion was not durably fenced")
	}
	deleter.chatID = chatID
	deleter.messageID = messageID
	return deleter.err
}

func (sender *checkingSender) Deliver(ctx context.Context, delivery interactionflow.Delivery) (interactionflow.DeliveryReceipt, error) {
	operation, found, err := sender.store.Load(ctx, delivery.OperationID)
	if err != nil || !found || operation.Phase != interactionflow.PhaseSendUnknown {
		return interactionflow.DeliveryReceipt{}, errors.New("delivery was not durably fenced")
	}
	sender.mu.Lock()
	sender.sent = append(sender.sent, delivery)
	if sender.wake != nil {
		close(sender.wake)
		sender.wake = nil
	}
	receipt, sendErr := sender.receipt, sender.err
	sender.mu.Unlock()
	receipt.OperationID = delivery.OperationID
	return receipt, sendErr
}

func (sender *checkingSender) wait(t *testing.T) interactionflow.Delivery {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		sender.mu.Lock()
		if len(sender.sent) > 0 {
			delivery := sender.sent[len(sender.sent)-1]
			sender.mu.Unlock()
			return delivery
		}
		if sender.wake == nil {
			sender.wake = make(chan struct{})
		}
		wake := sender.wake
		sender.mu.Unlock()
		select {
		case <-wake:
		case <-deadline:
			t.Fatal("delivery did not occur")
		}
	}
}

func (sender *checkingSender) calls() int {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return len(sender.sent)
}
