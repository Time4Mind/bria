package authflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/authflow"
)

const secretSentinel = "super-secret-auth-code"

func TestStartBindsChallengeAndIsReplaySafe(t *testing.T) {
	provider := &fakeAuthenticator{beginResult: authflow.BeginResult{
		ChallengeReference: "challenge-1",
		Instruction:        "Open the official provider page",
		ExpiresAt:          time.Date(2026, 9, 3, 12, 30, 0, 0, time.UTC),
	}}
	store := authflow.NewMemoryStore()
	service := mustService(t, store, provider, &fakeDeleter{})
	req := validStartRequest()

	first, err := service.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("start authorization: %v", err)
	}
	if first.Replayed || first.Status != authflow.StatusAwaitingAction || first.Instruction == "" {
		t.Fatalf("unexpected first start result: %#v", first)
	}

	second, err := service.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("replay start authorization: %v", err)
	}
	if !second.Replayed || second.Status != first.Status || second.ChallengeReference != first.ChallengeReference {
		t.Fatalf("unexpected replay result: %#v", second)
	}
	if second.Instruction != "" {
		t.Fatalf("transient instruction was persisted across replay: %q", second.Instruction)
	}
	if provider.beginCalls != 1 {
		t.Fatalf("provider begin calls = %d, want 1", provider.beginCalls)
	}

	record, ok, err := store.Load(context.Background(), req.OperationID)
	if err != nil || !ok {
		t.Fatalf("load persisted operation: ok=%v err=%v", ok, err)
	}
	if record.OwnerID != req.ActorID || record.PrivateChatID != req.ChatID || record.ComputerID != req.ComputerID || record.Provider != req.Provider {
		t.Fatalf("challenge binding was not persisted exactly: %#v", record)
	}

	conflict := req
	conflict.ComputerID = "computer-b"
	if _, err := service.Start(context.Background(), conflict); !errors.Is(err, authflow.ErrOperationConflict) {
		t.Fatalf("conflicting replay error = %v, want operation conflict", err)
	}
	if provider.beginCalls != 1 {
		t.Fatalf("conflicting replay reached provider: %d calls", provider.beginCalls)
	}
}

func TestSubmitDeletesSecretMessageOnEveryTerminalPath(t *testing.T) {
	tests := []struct {
		name          string
		mutate        func(*authflow.SubmitRequest)
		providerErr   error
		startExpiry   time.Time
		wantStatus    authflow.Status
		wantErr       error
		wantCompletes int
	}{
		{name: "authenticated", wantStatus: authflow.StatusAuthenticated, wantCompletes: 1},
		{name: "provider rejected", providerErr: fmt.Errorf("%w: provider leaked %s", authflow.ErrProviderRejected, secretSentinel), wantStatus: authflow.StatusRejected, wantErr: authflow.ErrAuthorizationRejected, wantCompletes: 1},
		{name: "expired", startExpiry: time.Date(2026, 9, 3, 11, 59, 0, 0, time.UTC), wantStatus: authflow.StatusExpired, wantErr: authflow.ErrChallengeExpired},
		{name: "wrong owner", mutate: func(r *authflow.SubmitRequest) { r.ActorID++ }, wantStatus: authflow.StatusAwaitingAction, wantErr: authflow.ErrUnauthorized},
		{name: "wrong chat", mutate: func(r *authflow.SubmitRequest) { r.ChatID++ }, wantStatus: authflow.StatusAwaitingAction, wantErr: authflow.ErrOperationConflict},
		{name: "wrong computer", mutate: func(r *authflow.SubmitRequest) { r.ComputerID = "computer-b" }, wantStatus: authflow.StatusAwaitingAction, wantErr: authflow.ErrOperationConflict},
		{name: "wrong provider", mutate: func(r *authflow.SubmitRequest) { r.Provider = authflow.ProviderClaude }, wantStatus: authflow.StatusAwaitingAction, wantErr: authflow.ErrOperationConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &fakeAuthenticator{beginResult: validBeginResult(), completeErr: tt.providerErr}
			if !tt.startExpiry.IsZero() {
				provider.beginResult.ExpiresAt = tt.startExpiry
			}
			deleter := &fakeDeleter{}
			service := mustService(t, authflow.NewMemoryStore(), provider, deleter)
			start, err := service.Start(context.Background(), validStartRequest())
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			req := validSubmitRequest(start.ChallengeReference)
			if tt.mutate != nil {
				tt.mutate(&req)
			}

			result, err := service.Submit(context.Background(), req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("submit error = %v, want %v", err, tt.wantErr)
			}
			if result.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", result.Status, tt.wantStatus)
			}
			if deleter.calls != 1 || deleter.lastChatID != req.ChatID || deleter.lastMessageID != req.MessageID {
				t.Fatalf("delete calls/ref = %d/(%d,%d), want 1/(%d,%d)", deleter.calls, deleter.lastChatID, deleter.lastMessageID, req.ChatID, req.MessageID)
			}
			if provider.completeCalls != tt.wantCompletes {
				t.Fatalf("provider complete calls = %d, want %d", provider.completeCalls, tt.wantCompletes)
			}
			if strings.Contains(fmt.Sprint(err), secretSentinel) {
				t.Fatalf("returned error leaks secret: %v", err)
			}
		})
	}
}

func TestSecretPayloadCannotBeSerializedOrFormatted(t *testing.T) {
	payload := authflow.NewSecretPayload([]byte(secretSentinel))
	for _, formatted := range []string{
		fmt.Sprint(payload),
		fmt.Sprintf("%v", payload),
		fmt.Sprintf("%+v", payload),
		fmt.Sprintf("%#v", payload),
	} {
		if strings.Contains(formatted, secretSentinel) {
			t.Fatalf("formatted payload leaks secret: %q", formatted)
		}
	}
	encoded, err := json.Marshal(payload)
	if err == nil {
		t.Fatalf("secret payload unexpectedly serialized as %s", encoded)
	}
}

func TestPersistedRecordContainsOnlyReferencesAndSafeStatus(t *testing.T) {
	provider := &fakeAuthenticator{beginResult: validBeginResult(), completeErr: fmt.Errorf("%w: provider echoed %s", authflow.ErrProviderRejected, secretSentinel)}
	store := &recordingStore{Store: authflow.NewMemoryStore()}
	service := mustService(t, store, provider, &fakeDeleter{})
	start, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_, _ = service.Submit(context.Background(), validSubmitRequest(start.ChallengeReference))

	if strings.Contains(store.serialized.String(), secretSentinel) {
		t.Fatalf("persisted state leaks secret: %s", store.serialized.String())
	}
	record, ok, err := store.Load(context.Background(), "auth-op-1")
	if err != nil || !ok {
		t.Fatalf("load record: ok=%v err=%v", ok, err)
	}
	if record.Status != authflow.StatusRejected || record.SubmissionOperationID != "auth-submit-1" || record.SecretMessageReference.MessageID != 99 {
		t.Fatalf("persisted references/status are incomplete: %#v", record)
	}
}

func TestSubmitReplayDoesNotRepeatProviderOrConfirmedDeletion(t *testing.T) {
	provider := &fakeAuthenticator{beginResult: validBeginResult()}
	deleter := &fakeDeleter{}
	service := mustService(t, authflow.NewMemoryStore(), provider, deleter)
	start, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	req := validSubmitRequest(start.ChallengeReference)

	first, err := service.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	second, err := service.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("replay submit: %v", err)
	}
	if !second.Replayed || second.Status != first.Status {
		t.Fatalf("unexpected replay result: %#v", second)
	}
	if provider.completeCalls != 1 || deleter.calls != 1 {
		t.Fatalf("replay repeated effects: provider=%d delete=%d", provider.completeCalls, deleter.calls)
	}

	conflict := req
	conflict.SubmissionOperationID = "auth-submit-other"
	conflict.MessageID = 100
	if _, err := service.Submit(context.Background(), conflict); !errors.Is(err, authflow.ErrOperationConflict) {
		t.Fatalf("conflicting submission error = %v, want operation conflict", err)
	}
	if deleter.calls != 2 {
		t.Fatalf("conflicting secret message was not deleted: calls=%d", deleter.calls)
	}
}

func TestUnconfirmedDeletionIsSanitizedAndRetriedWithoutRepeatingProvider(t *testing.T) {
	provider := &fakeAuthenticator{beginResult: validBeginResult()}
	deleter := &fakeDeleter{err: errors.New("telegram echoed " + secretSentinel)}
	store := authflow.NewMemoryStore()
	service := mustService(t, store, provider, deleter)
	start, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	req := validSubmitRequest(start.ChallengeReference)

	result, err := service.Submit(context.Background(), req)
	if !errors.Is(err, authflow.ErrDeletionUnconfirmed) || strings.Contains(fmt.Sprint(err), secretSentinel) {
		t.Fatalf("deletion error = %v, want sanitized unconfirmed", err)
	}
	if result.Status != authflow.StatusAuthenticated || result.Deletion != authflow.DeletionUnconfirmed {
		t.Fatalf("provider result lost when deletion failed: %#v", result)
	}

	deleter.err = nil
	replayed, err := service.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("retry deletion: %v", err)
	}
	if !replayed.Replayed || replayed.Deletion != authflow.DeletionConfirmed {
		t.Fatalf("unexpected deletion replay result: %#v", replayed)
	}
	if provider.completeCalls != 1 || deleter.calls != 2 {
		t.Fatalf("retry effects: provider=%d delete=%d", provider.completeCalls, deleter.calls)
	}
}

func TestTerminalAuthorizationMessageIsConsumedAfterRestartWithoutSecretReplay(t *testing.T) {
	for _, providerErr := range []error{nil, authflow.ErrProviderRejected} {
		provider := &fakeAuthenticator{beginResult: validBeginResult(), completeErr: providerErr}
		deleter := &fakeDeleter{}
		store := authflow.NewMemoryStore()
		service := mustService(t, store, provider, deleter)
		start, err := service.Start(context.Background(), validStartRequest())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = service.Submit(context.Background(), validSubmitRequest(start.ChallengeReference))
		restarted := mustService(t, store, provider, deleter)
		result, err := restarted.ConsumeMessage(context.Background(), authflow.MessageRequest{
			ActorID: 42, ChatID: 42, ConversationKind: "private", MessageID: 99,
		})
		if err != nil || !result.Bound || result.Deletion != authflow.DeletionConfirmed {
			t.Fatalf("ConsumeMessage() = (%#v, %v)", result, err)
		}
		if provider.completeCalls != 1 || deleter.calls != 1 {
			t.Fatalf("redelivery repeated effects: provider=%d delete=%d", provider.completeCalls, deleter.calls)
		}
	}
}

func TestTerminalAuthorizationMessageRetriesOnlyUnconfirmedDeletion(t *testing.T) {
	provider := &fakeAuthenticator{beginResult: validBeginResult()}
	deleter := &fakeDeleter{err: errors.New("ambiguous delete")}
	store := authflow.NewMemoryStore()
	service := mustService(t, store, provider, deleter)
	start, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = service.Submit(context.Background(), validSubmitRequest(start.ChallengeReference))
	deleter.err = nil
	result, err := service.ConsumeMessage(context.Background(), authflow.MessageRequest{
		ActorID: 42, ChatID: 42, ConversationKind: "private", MessageID: 99,
	})
	if err != nil || !result.Bound || result.Deletion != authflow.DeletionConfirmed {
		t.Fatalf("ConsumeMessage() = (%#v, %v)", result, err)
	}
	if provider.completeCalls != 1 || deleter.calls != 2 {
		t.Fatalf("consume repeated wrong effects: provider=%d delete=%d", provider.completeCalls, deleter.calls)
	}
}

func TestStartRejectsNonOwnerAndNonPrivateChatBeforeProvider(t *testing.T) {
	for _, mutate := range []func(*authflow.StartRequest){
		func(r *authflow.StartRequest) { r.ActorID++ },
		func(r *authflow.StartRequest) { r.ConversationKind = "group" },
	} {
		provider := &fakeAuthenticator{beginResult: validBeginResult()}
		service := mustService(t, authflow.NewMemoryStore(), provider, &fakeDeleter{})
		req := validStartRequest()
		mutate(&req)
		if _, err := service.Start(context.Background(), req); !errors.Is(err, authflow.ErrUnauthorized) {
			t.Fatalf("start error = %v, want unauthorized", err)
		}
		if provider.beginCalls != 0 {
			t.Fatalf("unauthorized start reached provider")
		}
	}
}

func TestStartProviderFailureIsSanitizedAndReplaySafe(t *testing.T) {
	provider := &fakeAuthenticator{beginErr: fmt.Errorf("%w: provider leaked %s", authflow.ErrProviderRejected, secretSentinel)}
	store := authflow.NewMemoryStore()
	service := mustService(t, store, provider, &fakeDeleter{})

	first, err := service.Start(context.Background(), validStartRequest())
	if !errors.Is(err, authflow.ErrAuthorizationRejected) || strings.Contains(fmt.Sprint(err), secretSentinel) {
		t.Fatalf("start error = %v, want sanitized rejection", err)
	}
	if first.Status != authflow.StatusRejected {
		t.Fatalf("start status = %q, want rejected", first.Status)
	}
	second, err := service.Start(context.Background(), validStartRequest())
	if !errors.Is(err, authflow.ErrAuthorizationRejected) || !second.Replayed {
		t.Fatalf("replay = (%#v, %v), want replayed rejection", second, err)
	}
	if provider.beginCalls != 1 {
		t.Fatalf("provider begin calls = %d, want 1", provider.beginCalls)
	}
}

func TestAmbiguousProviderOutcomesRemainRecoverable(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		provider := &fakeAuthenticator{beginResult: validBeginResult(), beginErr: errors.New("timeout leaked " + secretSentinel)}
		service := mustService(t, authflow.NewMemoryStore(), provider, &fakeDeleter{})
		first, err := service.Start(context.Background(), validStartRequest())
		if !errors.Is(err, authflow.ErrAuthorizationUnconfirmed) || first.Status != authflow.StatusStarting || strings.Contains(fmt.Sprint(err), secretSentinel) {
			t.Fatalf("ambiguous begin = (%#v, %v)", first, err)
		}
		provider.beginErr = nil
		recovered, err := service.Start(context.Background(), validStartRequest())
		if err != nil || !recovered.Replayed || recovered.Status != authflow.StatusAwaitingAction {
			t.Fatalf("recovered begin = (%#v, %v)", recovered, err)
		}
		if provider.beginCalls != 2 {
			t.Fatalf("begin calls = %d, want 2 with the same operation id", provider.beginCalls)
		}
	})

	t.Run("complete", func(t *testing.T) {
		provider := &fakeAuthenticator{beginResult: validBeginResult(), completeErr: errors.New("disconnect leaked " + secretSentinel)}
		deleter := &fakeDeleter{}
		service := mustService(t, authflow.NewMemoryStore(), provider, deleter)
		start, err := service.Start(context.Background(), validStartRequest())
		if err != nil {
			t.Fatal(err)
		}
		request := validSubmitRequest(start.ChallengeReference)
		first, err := service.Submit(context.Background(), request)
		if !errors.Is(err, authflow.ErrAuthorizationUnconfirmed) || first.Status != authflow.StatusCompleting || strings.Contains(fmt.Sprint(err), secretSentinel) {
			t.Fatalf("ambiguous complete = (%#v, %v)", first, err)
		}
		provider.completeErr = nil
		recovered, err := service.Submit(context.Background(), request)
		if err != nil || !recovered.Replayed || recovered.Status != authflow.StatusAuthenticated {
			t.Fatalf("recovered complete = (%#v, %v)", recovered, err)
		}
		if provider.completeCalls != 2 || deleter.calls != 1 {
			t.Fatalf("recovery effects: provider=%d delete=%d, want 2/1", provider.completeCalls, deleter.calls)
		}
	})
}

func TestUnauthorizedSubmitDoesNotRevealOperationExistence(t *testing.T) {
	provider := &fakeAuthenticator{beginResult: validBeginResult()}
	deleter := &fakeDeleter{}
	service := mustService(t, authflow.NewMemoryStore(), provider, deleter)
	request := validSubmitRequest("unknown")
	request.OperationID = "does-not-exist"
	request.ActorID++
	if _, err := service.Submit(context.Background(), request); !errors.Is(err, authflow.ErrUnauthorized) {
		t.Fatalf("unauthorized unknown operation error = %v", err)
	}
	if deleter.calls != 1 {
		t.Fatalf("secret message deletion calls = %d, want 1", deleter.calls)
	}
}

func TestSubmitDeletesUnknownInvalidAndCancelledMessages(t *testing.T) {
	provider := &fakeAuthenticator{beginResult: validBeginResult()}
	deleter := &contextCheckingDeleter{}
	service := mustService(t, authflow.NewMemoryStore(), provider, deleter)

	unknown := validSubmitRequest("challenge-1")
	unknown.OperationID = "unknown"
	if _, err := service.Submit(context.Background(), unknown); !errors.Is(err, authflow.ErrOperationNotFound) {
		t.Fatalf("unknown submit error = %v", err)
	}
	start, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	invalid := validSubmitRequest(start.ChallengeReference)
	invalid.Secret = authflow.SecretPayload{}
	if _, err := service.Submit(context.Background(), invalid); !errors.Is(err, authflow.ErrInvalidRequest) {
		t.Fatalf("invalid submit error = %v", err)
	}
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := validSubmitRequest(start.ChallengeReference)
	cancelled.SubmissionOperationID = "cancelled-submit"
	cancelled.MessageID = 101
	_, _ = service.Submit(cancelledContext, cancelled)

	if deleter.calls != 3 {
		t.Fatalf("delete calls = %d, want 3", deleter.calls)
	}
	if deleter.sawCancelled {
		t.Fatal("mandatory deletion inherited a cancelled request context")
	}
}

func TestConcurrentReplayPerformsProviderEffectsOnce(t *testing.T) {
	provider := &fakeAuthenticator{beginResult: validBeginResult()}
	deleter := &fakeDeleter{}
	service := mustService(t, authflow.NewMemoryStore(), provider, deleter)
	request := validStartRequest()

	const workers = 12
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer wait.Done()
			_, _ = service.Start(context.Background(), request)
		}()
	}
	wait.Wait()
	if provider.beginCalls != 1 {
		t.Fatalf("concurrent begin calls = %d, want 1", provider.beginCalls)
	}

	start, err := service.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("read start: %v", err)
	}
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer wait.Done()
			_, _ = service.Submit(context.Background(), validSubmitRequest(start.ChallengeReference))
		}()
	}
	wait.Wait()
	if provider.completeCalls != 1 || deleter.calls != 1 {
		t.Fatalf("concurrent effects: complete=%d delete=%d, want 1/1", provider.completeCalls, deleter.calls)
	}
}

func TestPersistedAuthorizationHasNoEnabledState(t *testing.T) {
	provider := &fakeAuthenticator{beginResult: validBeginResult()}
	store := authflow.NewMemoryStore()
	service := mustService(t, store, provider, &fakeDeleter{})
	start, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Submit(context.Background(), validSubmitRequest(start.ChallengeReference)); err != nil {
		t.Fatal(err)
	}
	record, ok, err := store.Load(context.Background(), validStartRequest().OperationID)
	if err != nil || !ok {
		t.Fatalf("load authorization: ok=%v err=%v", ok, err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "enabled") {
		t.Fatalf("authorization state contains provider availability: %s", raw)
	}
}

func TestStoreCASCannotRebindAuthorizationToAnotherComputer(t *testing.T) {
	store := authflow.NewMemoryStore()
	service := mustService(t, store, &fakeAuthenticator{beginResult: validBeginResult()}, &fakeDeleter{})
	if _, err := service.Start(context.Background(), validStartRequest()); err != nil {
		t.Fatal(err)
	}
	record, ok, err := store.Load(context.Background(), validStartRequest().OperationID)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	rebound := record
	rebound.ComputerID = "computer-b"
	if _, swapped, err := store.CompareAndSwap(context.Background(), record.OperationID, record.Revision, rebound); !errors.Is(err, authflow.ErrOperationConflict) || swapped {
		t.Fatalf("binding mutation = swapped %v, error %v", swapped, err)
	}
}

func TestStartRecoversPersistedStartingClaimWithSameOperationID(t *testing.T) {
	store := authflow.NewMemoryStore()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	request := validStartRequest()
	if _, created, err := store.Create(context.Background(), authflow.Record{
		OperationID: request.OperationID, OwnerID: request.ActorID, PrivateChatID: request.ChatID,
		ComputerID: request.ComputerID, Provider: request.Provider, Status: authflow.StatusStarting,
		Deletion: authflow.DeletionNotRequired, CreatedAt: now, UpdatedAt: now,
	}); err != nil || !created {
		t.Fatalf("persist interrupted claim: created=%v err=%v", created, err)
	}
	provider := &fakeAuthenticator{beginResult: validBeginResult()}
	service := mustService(t, store, provider, &fakeDeleter{})

	result, err := service.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("recover start: %v", err)
	}
	if result.Status != authflow.StatusAwaitingAction || result.Instruction == "" {
		t.Fatalf("recovered result = %#v", result)
	}
	if provider.beginCalls != 1 {
		t.Fatalf("provider begin calls = %d, want idempotent recovery call", provider.beginCalls)
	}
}

func TestStartReplayMarksUnansweredChallengeExpired(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	provider := &fakeAuthenticator{beginResult: validBeginResult()}
	service, err := authflow.NewService(42, authflow.NewMemoryStore(), map[authflow.Provider]authflow.Authenticator{
		authflow.ProviderCodex: provider,
	}, &fakeDeleter{}, authflow.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(context.Background(), validStartRequest()); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 9, 3, 12, 31, 0, 0, time.UTC)
	replayed, err := service.Start(context.Background(), validStartRequest())
	if !errors.Is(err, authflow.ErrChallengeExpired) || !replayed.Replayed || replayed.Status != authflow.StatusExpired {
		t.Fatalf("expired replay = (%#v, %v)", replayed, err)
	}
	if provider.beginCalls != 1 {
		t.Fatalf("expired replay called provider %d times", provider.beginCalls)
	}
}

type fakeAuthenticator struct {
	beginResult   authflow.BeginResult
	beginErr      error
	completeErr   error
	beginCalls    int
	completeCalls int
	lastComplete  authflow.CompleteRequest
}

func (f *fakeAuthenticator) Begin(context.Context, authflow.BeginRequest) (authflow.BeginResult, error) {
	f.beginCalls++
	return f.beginResult, f.beginErr
}

func (f *fakeAuthenticator) Complete(_ context.Context, request authflow.CompleteRequest) error {
	f.completeCalls++
	f.lastComplete = request
	return f.completeErr
}

type fakeDeleter struct {
	calls         int
	lastChatID    int64
	lastMessageID int64
	err           error
}

type contextCheckingDeleter struct {
	calls        int
	sawCancelled bool
}

func (deleter *contextCheckingDeleter) DeleteMessage(ctx context.Context, _, _ int64) error {
	deleter.calls++
	deleter.sawCancelled = deleter.sawCancelled || ctx.Err() != nil
	return nil
}

func (f *fakeDeleter) DeleteMessage(_ context.Context, chatID, messageID int64) error {
	f.calls++
	f.lastChatID = chatID
	f.lastMessageID = messageID
	return f.err
}

type recordingStore struct {
	authflow.Store
	serialized strings.Builder
}

func (s *recordingStore) Create(ctx context.Context, record authflow.Record) (authflow.Record, bool, error) {
	s.capture(record)
	return s.Store.Create(ctx, record)
}

func (s *recordingStore) CompareAndSwap(ctx context.Context, operationID string, revision uint64, next authflow.Record) (authflow.Record, bool, error) {
	s.capture(next)
	return s.Store.CompareAndSwap(ctx, operationID, revision, next)
}

func (s *recordingStore) capture(record authflow.Record) {
	encoded, err := json.Marshal(record)
	if err != nil {
		panic(err)
	}
	s.serialized.Write(encoded)
}

func mustService(t *testing.T, store authflow.Store, provider authflow.Authenticator, deleter authflow.TelegramDeleter) *authflow.Service {
	t.Helper()
	service, err := authflow.NewService(42, store, map[authflow.Provider]authflow.Authenticator{
		authflow.ProviderCodex:  provider,
		authflow.ProviderClaude: provider,
	}, deleter, authflow.WithClock(func() time.Time {
		return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	}))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

func validStartRequest() authflow.StartRequest {
	return authflow.StartRequest{
		OperationID:      "auth-op-1",
		ActorID:          42,
		ChatID:           42,
		ConversationKind: "private",
		ComputerID:       "computer-a",
		Provider:         authflow.ProviderCodex,
	}
}

func validSubmitRequest(challengeReference string) authflow.SubmitRequest {
	return authflow.SubmitRequest{
		OperationID:           "auth-op-1",
		SubmissionOperationID: "auth-submit-1",
		ActorID:               42,
		ChatID:                42,
		ConversationKind:      "private",
		MessageID:             99,
		ComputerID:            "computer-a",
		Provider:              authflow.ProviderCodex,
		ChallengeReference:    challengeReference,
		Secret:                authflow.NewSecretPayload([]byte(secretSentinel)),
	}
}

func TestPendingRestoresOnlyExactOwnerPrivateNonTerminalOperations(t *testing.T) {
	store := authflow.NewMemoryStore()
	provider := &fakeAuthenticator{beginResult: validBeginResult()}
	service := mustService(t, store, provider, &fakeDeleter{})
	started, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	pending, err := service.Pending(context.Background(), authflow.PendingRequest{
		ActorID: 42, ChatID: 42, ConversationKind: "private",
	})
	if err != nil || len(pending) != 1 {
		t.Fatalf("Pending() = (%#v, %v), want one", pending, err)
	}
	if pending[0].OperationID != started.OperationID || pending[0].ChallengeReference != started.ChallengeReference || pending[0].Status != authflow.StatusAwaitingAction {
		t.Fatalf("pending operation = %#v", pending[0])
	}
	if _, err := service.Pending(context.Background(), authflow.PendingRequest{ActorID: 41, ChatID: 42, ConversationKind: "private"}); !errors.Is(err, authflow.ErrUnauthorized) {
		t.Fatalf("non-owner Pending() error = %v", err)
	}
}

func TestDiscardDeletesUnmatchedSecretWithoutReceivingItsBody(t *testing.T) {
	deleter := &fakeDeleter{}
	service := mustService(t, authflow.NewMemoryStore(), &fakeAuthenticator{beginResult: validBeginResult()}, deleter)
	result, err := service.Discard(context.Background(), authflow.DiscardRequest{
		OperationID: "telegram-message:912:delete", ActorID: 42, ChatID: 42, ConversationKind: "private", MessageID: 912,
	})
	if err != nil || result.Deletion != authflow.DeletionConfirmed {
		t.Fatalf("Discard() = (%#v, %v)", result, err)
	}
	if deleter.calls != 1 || deleter.lastChatID != 42 || deleter.lastMessageID != 912 {
		t.Fatalf("delete calls/target = %d/%d/%d", deleter.calls, deleter.lastChatID, deleter.lastMessageID)
	}
}

func validBeginResult() authflow.BeginResult {
	return authflow.BeginResult{
		ChallengeReference: "challenge-1",
		Instruction:        "Open the official provider page",
		ExpiresAt:          time.Date(2026, 9, 3, 12, 30, 0, 0, time.UTC),
	}
}
