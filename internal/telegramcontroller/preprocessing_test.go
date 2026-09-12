package telegramcontroller_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/promptpreprocess"
	"bria/internal/sessionruntime"
	"bria/internal/settingsport"
	"bria/internal/telegramcontroller"
	"bria/internal/turnprocessing"
)

type promptProcessorFunc func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error)

func (function promptProcessorFunc) Process(ctx context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	return function(ctx, request)
}

type promptObserverFunc func(context.Context, promptpreprocess.Observation) error

func (function promptObserverFunc) ObservePreprocessing(ctx context.Context, observation promptpreprocess.Observation) error {
	return function(ctx, observation)
}

type preprocessingCompletionFunc func(context.Context)

func (function preprocessingCompletionFunc) Accept(ctx context.Context) error {
	function(ctx)
	return nil
}

type preprocessingBarrierCustody struct {
	accepted       chan telegramcontroller.OutgoingNotification
	release        chan struct{}
	acceptErr      error
	waitErr        error
	rejectDeadline bool
}

func (custody *preprocessingBarrierCustody) AcceptOutput(_ context.Context, output telegramcontroller.OutgoingNotification) (telegramcontroller.OutputReceipt, error) {
	custody.accepted <- output
	if custody.acceptErr != nil {
		return telegramcontroller.OutputReceipt{}, custody.acceptErr
	}
	return telegramcontroller.OutputReceipt{Inserted: true, SessionID: output.SessionID, OperationID: output.OperationID, Sequence: 1}, nil
}

func (custody *preprocessingBarrierCustody) WaitOutputDelivery(ctx context.Context, _ domain.SessionID, _ string) error {
	if _, hasDeadline := ctx.Deadline(); custody.rejectDeadline && hasDeadline {
		return errors.New("unexpected internal prompt delivery timeout")
	}
	if custody.waitErr != nil {
		return custody.waitErr
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-custody.release:
		return nil
	}
}

func TestDurablePreprocessingDoesNotStartProviderWithoutConfirmedPromptDelivery(t *testing.T) {
	for _, test := range []struct {
		name      string
		acceptErr error
		waitErr   error
		timeout   bool
	}{
		{name: "enqueue failure", acceptErr: errors.New("synthetic enqueue failure")},
		{name: "unknown receipt", waitErr: errors.New("synthetic unknown delivery")},
		{name: "delivery timeout", timeout: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ready := readySession(t, "aaaabaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-visible", 1)
			payload, err := promptpreprocess.Encode("clean", "raw speech")
			if err != nil {
				t.Fatal(err)
			}
			custody := &preprocessingBarrierCustody{
				accepted:  make(chan telegramcontroller.OutgoingNotification, 1),
				release:   make(chan struct{}),
				acceptErr: test.acceptErr,
				waitErr:   test.waitErr,
			}
			providerCalled := false
			controller := newController(t, nil, newLockedSessions(ready), &interactiveSubmitter{submitWithCallbacks: func(context.Context, domain.SessionID, string, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
				providerCalled = true
				return sessionruntime.TurnResult{}, nil
			}}, nil, telegramcontroller.Options{
				Recovered: []domain.Session{ready}, DurableOutput: custody,
				Preprocessor: promptProcessorFunc(func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
					return promptpreprocess.Result{Text: "cleaned prompt"}, nil
				}),
			})
			t.Cleanup(func() { _ = controller.Close(context.Background()) })
			ctx := context.Background()
			if test.timeout {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			receipt, processErr := controller.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{
				SessionID: ready.ID(), MessageID: "telegram-update:512", Sequence: 1, Payload: payload,
			}, telegramcontroller.DurableInputCallbacks{
				OnPrepared: func(context.Context, telegramcontroller.DurableInputPreparation) error { return nil },
				OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
			})
			if receipt.Accepted || !errors.Is(processErr, turnprocessing.ErrInputDeferred) || providerCalled {
				t.Fatalf("unconfirmed prompt delivery = receipt:%#v err:%v provider:%t", receipt, processErr, providerCalled)
			}
		})
	}
}

func TestDurablePreprocessingWaitsForVisiblePromptDeliveryBeforeProviderStarts(t *testing.T) {
	ready := readySession(t, "aaabaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-visible", 1)
	payload, err := promptpreprocess.Encode("clean", "raw speech")
	if err != nil {
		t.Fatal(err)
	}
	custody := &preprocessingBarrierCustody{accepted: make(chan telegramcontroller.OutgoingNotification, 3), release: make(chan struct{}), rejectDeadline: true}
	providerStarted := make(chan struct{}, 1)
	controller := newController(t, nil, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, _ string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		providerStarted <- struct{}{}
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "done"}, nil
	}}, nil, telegramcontroller.Options{
		Recovered: []domain.Session{ready}, DurableOutput: custody,
		Preprocessor: promptProcessorFunc(func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
			return promptpreprocess.Result{Text: "cleaned prompt", Provider: domain.ProviderCodex, Model: "cheap"}, nil
		}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	done := make(chan error, 1)
	go func() {
		_, processErr := controller.ProcessDurableInput(context.Background(), telegramcontroller.DurableLeasedInput{
			SessionID: ready.ID(), MessageID: "telegram-update:511", Sequence: 1, Payload: payload,
		}, telegramcontroller.DurableInputCallbacks{
			OnPrepared: func(context.Context, telegramcontroller.DurableInputPreparation) error { return nil },
			OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
		})
		done <- processErr
	}()
	select {
	case output := <-custody.accepted:
		if output.OperationID != "telegram-update:511:prompt-status:preprocessed" {
			t.Fatalf("first output = %#v", output)
		}
	case <-time.After(time.Second):
		t.Fatal("preprocessed prompt was not placed in durable delivery")
	}
	select {
	case <-providerStarted:
		t.Fatal("provider started before preprocessed prompt delivery completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(custody.release)
	select {
	case <-providerStarted:
	case <-time.After(time.Second):
		t.Fatal("provider did not start after prompt delivery completed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("durable input did not complete")
	}
}

func TestDurablePreprocessingPersistsCleanedTextBeforeProviderAcceptance(t *testing.T) {
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
	payload, err := promptpreprocess.Encode("clean", "raw speech")
	if err != nil {
		t.Fatal(err)
	}
	ui := &projectionUIState{}
	providerText := ""
	preparedBeforeAccepted := false
	preprocessingAccepted := false
	notifications := make(chan telegramcontroller.Notification, 3)
	interactive := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		providerText = text
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "done"}, nil
	}}
	controller := newController(t, nil, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, interactive, notifierFunc(func(_ context.Context, notification telegramcontroller.Notification) error {
		notifications <- notification
		return nil
	}),
		telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: ui, Preprocessor: promptProcessorFunc(func(_ context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
			if request.Text != "raw speech" || request.Instruction != "clean" || request.SessionID != ready.ID() || request.Sequence != 1 {
				t.Fatalf("preprocessing request = %#v", request)
			}
			return promptpreprocess.Result{
				Text: "cleaned prompt", Provider: domain.ProviderCodex, Model: "cheap",
				Completion: preprocessingCompletionFunc(func(context.Context) {
					if !preparedBeforeAccepted {
						t.Fatal("preprocessing session closed before prepared payload was durable")
					}
					preprocessingAccepted = true
				}),
			}, nil
		}), PreprocessingObserver: promptObserverFunc(func(context.Context, promptpreprocess.Observation) error {
			if !preparedBeforeAccepted {
				t.Fatal("preprocessing completion was logged before prepared payload was durable")
			}
			return nil
		})})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	receipt, err := controller.ProcessDurableInput(context.Background(), telegramcontroller.DurableLeasedInput{
		SessionID: ready.ID(), MessageID: "telegram-update:501", Sequence: 1, Payload: payload,
	}, telegramcontroller.DurableInputCallbacks{
		OnPrepared: func(_ context.Context, prepared telegramcontroller.DurableInputPreparation) error {
			state, decodeErr := promptpreprocess.DecodeState(prepared.Payload)
			if decodeErr != nil || !state.Prepared || state.Failed || state.Original != "raw speech" || state.Processed != "cleaned prompt" {
				t.Fatalf("prepared state = %#v, %v", state, decodeErr)
			}
			preparedBeforeAccepted = true
			return nil
		},
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error {
			if !preparedBeforeAccepted {
				t.Fatal("provider accepted before preprocessing result was persisted")
			}
			return nil
		},
	})
	if err != nil || !receipt.Accepted || providerText != "cleaned prompt" || !preprocessingAccepted {
		t.Fatalf("durable preprocessing = (%#v, %v), provider text %q", receipt, err, providerText)
	}
	first, second := <-notifications, <-notifications
	if first.OperationID != "telegram-update:501:prompt-status:preprocessed" || second.OperationID != "telegram-update:501:prompt-status:👨‍💻" {
		t.Fatalf("prompt transition notifications = %#v, %#v", first, second)
	}
	select {
	case final := <-notifications:
		if final.Kind != telegramcontroller.NotificationFinal {
			t.Fatalf("completion notification = %#v", final)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted turn did not complete")
	}
	history, err := ui.LoadCardHistory(context.Background(), ready.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(history) < 1 || !strings.HasPrefix(history[0], "👨") || !strings.Contains(history[0], "cleaned prompt") {
		t.Fatalf("card history = %#v", history)
	}
}

func TestDurablePreprocessingPersistenceFailureDoesNotRetireTechnicalSession(t *testing.T) {
	ready := readySession(t, "abababab-abab-4bab-9bab-abababababab", domain.ProviderCodex, t.TempDir(), "provider-persist", 1)
	payload, err := promptpreprocess.Encode("clean", "raw speech")
	if err != nil {
		t.Fatal(err)
	}
	retired := false
	providerCalled := false
	controller := newController(t, nil, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, &interactiveSubmitter{submitWithCallbacks: func(context.Context, domain.SessionID, string, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		providerCalled = true
		return sessionruntime.TurnResult{}, nil
	}}, nil, telegramcontroller.Options{
		Recovered: []domain.Session{ready},
		Preprocessor: promptProcessorFunc(func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
			return promptpreprocess.Result{
				Text: "cleaned", Completion: preprocessingCompletionFunc(func(context.Context) { retired = true }),
			}, nil
		}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	sentinel := errors.New("persistence unavailable")
	_, err = controller.ProcessDurableInput(context.Background(), telegramcontroller.DurableLeasedInput{
		SessionID: ready.ID(), MessageID: "telegram-update:599", Sequence: 9, Payload: payload,
	}, telegramcontroller.DurableInputCallbacks{
		OnPrepared: func(context.Context, telegramcontroller.DurableInputPreparation) error { return sentinel },
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
	})
	if !errors.Is(err, sentinel) || retired || providerCalled {
		t.Fatalf("persist failure = err:%v retired:%t provider_called:%t", err, retired, providerCalled)
	}
}

func TestDurablePreprocessingFailurePersistsAndSendsOriginal(t *testing.T) {
	ready := readySession(t, "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb", domain.ProviderClaude, t.TempDir(), "provider-b", 1)
	payload, err := promptpreprocess.Encode("clean", "original request")
	if err != nil {
		t.Fatal(err)
	}
	ui := &projectionUIState{}
	providerText := ""
	var observed promptpreprocess.Observation
	interactive := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		providerText = text
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted}, nil
	}}
	controller := newController(t, nil, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, interactive, nil,
		telegramcontroller.Options{
			Recovered: []domain.Session{ready}, UIState: ui,
			Preprocessor: promptProcessorFunc(func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
				return promptpreprocess.Result{Provider: domain.ProviderCodex, Model: "cheap"}, errors.New("provider unavailable")
			}),
			PreprocessingObserver: promptObserverFunc(func(_ context.Context, observation promptpreprocess.Observation) error {
				observed = observation
				return nil
			}),
		})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	preparedFailed := false
	receipt, err := controller.ProcessDurableInput(context.Background(), telegramcontroller.DurableLeasedInput{
		SessionID: ready.ID(), MessageID: "telegram-update:502", Sequence: 2, Payload: payload,
	}, telegramcontroller.DurableInputCallbacks{
		OnPrepared: func(_ context.Context, prepared telegramcontroller.DurableInputPreparation) error {
			state, decodeErr := promptpreprocess.DecodeState(prepared.Payload)
			preparedFailed = decodeErr == nil && state.Prepared && state.Failed && state.Processed == "original request"
			return nil
		},
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
	})
	if err != nil || !receipt.Accepted || providerText != "original request" || !preparedFailed {
		t.Fatalf("fallback = (%#v, %v), text=%q prepared=%v", receipt, err, providerText, preparedFailed)
	}
	history, err := ui.LoadCardHistory(context.Background(), ready.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(history) < 1 || !strings.HasPrefix(history[0], "❌ Ошибка препроцессинга\n👨") || !strings.Contains(history[0], "original request") {
		t.Fatalf("card history = %#v", history)
	}
	if observed.SessionID != ready.ID() || observed.MessageID != "telegram-update:502" || observed.Attempts != 1 || observed.Category != "provider" || strings.Contains(observed.Error, "original request") {
		t.Fatalf("safe observation = %#v", observed)
	}
}

func TestPreparedPreprocessingReplayDoesNotCallModelAgain(t *testing.T) {
	ready := readySession(t, "cccccccc-cccc-4ccc-9ccc-cccccccccccc", domain.ProviderCodex, t.TempDir(), "provider-c", 1)
	payload, err := promptpreprocess.Encode("clean", "raw")
	if err != nil {
		t.Fatal(err)
	}
	payload, err = promptpreprocess.MarkPrepared(payload, "stable cleaned", false)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	providerText := ""
	interactive := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		providerText = text
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted}, nil
	}}
	controller := newController(t, nil, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, interactive, nil,
		telegramcontroller.Options{Recovered: []domain.Session{ready}, Preprocessor: promptProcessorFunc(func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
			calls++
			return promptpreprocess.Result{}, nil
		})})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	_, err = controller.ProcessDurableInput(context.Background(), telegramcontroller.DurableLeasedInput{
		SessionID: ready.ID(), MessageID: "telegram-update:503", Sequence: 3, Payload: payload,
	}, telegramcontroller.DurableInputCallbacks{
		OnPrepared: func(context.Context, telegramcontroller.DurableInputPreparation) error {
			t.Fatal("already prepared payload was persisted again")
			return nil
		},
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
	})
	if err != nil || calls != 0 || providerText != "stable cleaned" {
		t.Fatalf("replay err=%v calls=%d text=%q", err, calls, providerText)
	}
}

func TestPreprocessingTimeoutFallsBackWithoutSecondCall(t *testing.T) {
	ready := readySession(t, "dddddddd-dddd-4ddd-9ddd-dddddddddddd", domain.ProviderCodex, t.TempDir(), "provider-d", 1)
	payload, err := promptpreprocess.Encode("clean", "raw")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	providerText := ""
	interactive := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		providerText = text
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted}, nil
	}}
	controller := newController(t, nil, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, interactive, nil,
		telegramcontroller.Options{Recovered: []domain.Session{ready}, PreprocessingTimeout: 5 * time.Millisecond,
			Preprocessor: promptProcessorFunc(func(ctx context.Context, _ promptpreprocess.Request) (promptpreprocess.Result, error) {
				calls++
				<-ctx.Done()
				return promptpreprocess.Result{Provider: domain.ProviderCodex, Model: "cheap"}, ctx.Err()
			})})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	_, err = controller.ProcessDurableInput(context.Background(), telegramcontroller.DurableLeasedInput{
		SessionID: ready.ID(), MessageID: "telegram-update:504", Sequence: 4, Payload: payload,
	}, telegramcontroller.DurableInputCallbacks{
		OnPrepared: func(context.Context, telegramcontroller.DurableInputPreparation) error { return nil },
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
	})
	if err != nil || calls != 1 || providerText != "raw" {
		t.Fatalf("timeout fallback err=%v calls=%d text=%q", err, calls, providerText)
	}
}

func TestIngressNativeCommandsNeverEnterPromptPreprocessing(t *testing.T) {
	ready := readySession(t, "eeeeeeee-eeee-4eee-9eee-eeeeeeeeeeee", domain.ProviderCodex, t.TempDir(), "provider-e", 1)
	preferences := &testPreferences{settings: settingsport.Snapshot{
		ContinueExisting: true, CardDetail: "standard", CardPageLimit: 64, ShowTechnicalActions: true,
		SessionLifetime: "never", QueueLimit: 32, VoiceRecognition: "parakeet", PreprocessingEnabled: true,
	}}
	var accepted []telegramcontroller.SessionInput
	controller := newController(t, nil, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, nil, nil,
		telegramcontroller.Options{Recovered: []domain.Session{ready}, Settings: preferences,
			DurableInput: durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
				accepted = append(accepted, input)
				return telegramcontroller.InputReceipt{Inserted: true, SessionID: input.SessionID, MessageID: input.MessageID, Sequence: uint64(len(accepted))}, nil
			})})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	if _, err := controller.Handle(context.Background(), coordinator.Update{ID: 599, Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 42, ConversationKind: "private", Text: "/use " + string(ready.ID())}); err != nil {
		t.Fatal(err)
	}
	for index, text := range []string{"/custom one two three", "/custom one two three four"} {
		_, err := controller.Handle(context.Background(), coordinator.Update{ID: int64(600 + index), Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 42, ConversationKind: "private", Text: text})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(accepted) != 0 {
		t.Fatalf("accepted inputs = %d", len(accepted))
	}
}

func TestVoiceTranscriptEntersDurablePreprocessingEnvelope(t *testing.T) {
	ready := readySession(t, "ffffffff-ffff-4fff-9fff-ffffffffffff", domain.ProviderCodex, t.TempDir(), "provider-f", 1)
	preferences := &testPreferences{settings: settingsport.Snapshot{
		ContinueExisting: true, CardDetail: "standard", CardPageLimit: 64, ShowTechnicalActions: true,
		SessionLifetime: "never", QueueLimit: 32, VoiceRecognition: "parakeet", PreprocessingEnabled: true,
	}}
	acceptedInputs := make(chan telegramcontroller.SessionInput, 1)
	controller := newController(t, nil, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, nil, nil,
		telegramcontroller.Options{
			Recovered: []domain.Session{ready}, Settings: preferences,
			InputPreparer: inputPreparerFunc(func(_ context.Context, input telegramcontroller.IncomingInput) (string, error) {
				if input.Kind != "voice" {
					t.Fatalf("prepared kind = %q", input.Kind)
				}
				return "распознанный текст", nil
			}),
			DurableInput: durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
				acceptedInputs <- input
				return telegramcontroller.InputReceipt{Inserted: true, SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 1}, nil
			}),
		})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(801, "/use "+string(ready.ID())))
	voice := message(802, "")
	voice.Caption = "контекст"
	voice.MediaKind = "voice"
	voice.MediaFileID = "voice-file"
	voice.MediaDownloadAllowed = true
	mustStatus(t, controller, voice)
	var accepted telegramcontroller.SessionInput
	select {
	case accepted = <-acceptedInputs:
	case <-time.After(time.Second):
		t.Fatal("voice preprocessing did not reach durable input")
	}
	state, err := promptpreprocess.DecodeState(accepted.Payload)
	if err != nil || !state.Enabled || state.Instruction != promptpreprocess.DefaultInstruction || state.Original != "контекст\n\nраспознанный текст" {
		t.Fatalf("voice preprocessing state = %#v, %v", state, err)
	}
}

func TestPhotoCaptionIsWrappedWithoutChangingDurableAttachment(t *testing.T) {
	ready := readySession(t, "99999999-9999-4999-9999-999999999999", domain.ProviderClaude, t.TempDir(), "provider-9", 1)
	wantAttachment := telegramcontroller.AttachmentRef{
		Reference: "photo-custody-99", Size: 2048,
		SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	preferences := &testPreferences{settings: settingsport.Snapshot{
		ContinueExisting: true, CardDetail: "standard", CardPageLimit: 64, ShowTechnicalActions: true,
		SessionLifetime: "never", QueueLimit: 32, VoiceRecognition: "parakeet", PreprocessingEnabled: true,
	}}
	var accepted telegramcontroller.SessionInput
	controller := newController(t, nil, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, nil, nil,
		telegramcontroller.Options{
			Recovered: []domain.Session{ready}, Settings: preferences,
			InputPreparer: structuredPreparer{prepared: telegramcontroller.PreparedInput{Text: "описание фото", Attachments: []telegramcontroller.AttachmentRef{wantAttachment}}},
			DurableInput: durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
				accepted = input
				return telegramcontroller.InputReceipt{Inserted: true, SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 1}, nil
			}),
		})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(803, "/use "+string(ready.ID())))
	photo := message(804, "проверь")
	photo.MediaKind = "photo"
	photo.MediaFileID = "photo-file"
	photo.MediaDownloadAllowed = true
	mustStatus(t, controller, photo)
	state, err := promptpreprocess.DecodeState(accepted.Payload)
	if err != nil || !state.Enabled || state.Original != "проверь\n\nописание фото" {
		t.Fatalf("photo preprocessing state = %#v, %v", state, err)
	}
	if !reflect.DeepEqual(accepted.Attachments, []telegramcontroller.AttachmentRef{wantAttachment}) {
		t.Fatalf("durable attachments = %#v", accepted.Attachments)
	}
}

func TestPreprocessingInstructionEditConsumesOneTextMessageAndCanBeCancelled(t *testing.T) {
	preferences := &testPreferences{settings: settingsport.Snapshot{
		CardDetail: "standard", CardPageLimit: 64,
		PreprocessingInstruction: "Keep names, dates, and commands exactly.",
	}}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{Settings: preferences})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsPreprocessingInstruction})
	if err != nil || result.Surface == nil || !result.Surface.RichMarkdown ||
		!strings.Contains(result.Surface.Text, "новую инструкцию") {
		t.Fatalf("edit surface = (%#v, %v)", result, err)
	}
	if got := copyableInstructionBody(t, result.Surface.Text); got != "Keep names, dates, and commands exactly." {
		t.Fatalf("copyable instruction = %q", got)
	}
	if _, err := controller.HandleSemanticMessage(context.Background(), coordinator.Update{ID: 701, Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 42, ConversationKind: "private", Text: "  keep intent  "}); err != nil {
		t.Fatal(err)
	}
	if preferences.settings.PreprocessingInstruction != "keep intent" {
		t.Fatalf("saved instruction = %q", preferences.settings.PreprocessingInstruction)
	}
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsPreprocessingInstruction}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuSettings}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.HandleSemanticMessage(context.Background(), coordinator.Update{ID: 702, Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 42, ConversationKind: "private", Text: "must not replace"}); err != nil {
		t.Fatal(err)
	}
	if preferences.settings.PreprocessingInstruction != "keep intent" {
		t.Fatalf("cancelled edit changed instruction to %q", preferences.settings.PreprocessingInstruction)
	}
}

func TestPreprocessingInstructionEditShowsEffectiveBuiltInInstruction(t *testing.T) {
	preferences := &testPreferences{settings: settingsport.Snapshot{CardDetail: "standard", CardPageLimit: 64}}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{Settings: preferences})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsPreprocessingInstruction})
	if err != nil || result.Surface == nil || !result.Surface.RichMarkdown {
		t.Fatalf("built-in instruction surface = (%#v, %v)", result, err)
	}
	if got := copyableInstructionBody(t, result.Surface.Text); got != promptpreprocess.DefaultInstruction {
		t.Fatalf("copyable built-in instruction changed: %q", got)
	}
}

func TestPreprocessingInstructionEditPaginatesMaximumCopyableInstruction(t *testing.T) {
	instruction := strings.TrimSuffix(strings.Repeat("абвгд\n", 1489), "\n")
	if len([]byte(instruction)) > 16*1024 {
		t.Fatalf("test instruction exceeds settings contract: %d bytes", len([]byte(instruction)))
	}
	preferences := &testPreferences{settings: settingsport.Snapshot{
		CardDetail: "standard", CardPageLimit: 64, PreprocessingInstruction: instruction,
	}}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{Settings: preferences})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	var rebuilt strings.Builder
	action := telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsPreprocessingInstruction}
	for page := 1; ; page++ {
		result, err := controller.HandleSemanticAction(context.Background(), action)
		if err != nil || result.Surface == nil {
			t.Fatalf("instruction page %d = (%#v, %v)", page, result, err)
		}
		if !result.Surface.RichMarkdown || len([]byte(result.Surface.Text)) > 4096 {
			t.Fatalf("instruction page %d shape: rich=%t bytes=%d", page, result.Surface.RichMarkdown, len([]byte(result.Surface.Text)))
		}
		rebuilt.WriteString(copyableInstructionBody(t, result.Surface.Text))

		nextChoice := 0
		for _, row := range result.Surface.Rows {
			for _, button := range row {
				if button.Label == "Следующая" && button.Action == telegramcontroller.SemanticSettingsPreprocessingInstruction {
					nextChoice = button.Choice
				}
			}
		}
		if nextChoice == 0 {
			break
		}
		action.Choice = nextChoice
	}
	if rebuilt.String() != instruction {
		t.Fatalf("paginated instruction changed: got %d bytes, want %d", rebuilt.Len(), len([]byte(instruction)))
	}
}

func copyableInstructionBody(t *testing.T, text string) string {
	t.Helper()
	const prefixEnd = ":\n\n"
	start := strings.Index(text, prefixEnd)
	end := strings.LastIndex(text, "\n\nОтправьте новую инструкцию")
	if start < 0 || end < start {
		t.Fatalf("instruction surface has no copyable block: %q", text)
	}
	block := text[start+len(prefixEnd) : end]
	firstNewline := strings.IndexByte(block, '\n')
	lastNewline := strings.LastIndexByte(block, '\n')
	if firstNewline < 3 || lastNewline <= firstNewline {
		t.Fatalf("instruction surface is not fenced: %q", text)
	}
	fence := block[:firstNewline]
	if strings.Trim(fence, "`") != "" || block[lastNewline+1:] != fence {
		t.Fatalf("instruction surface fence is invalid: %q", text)
	}
	return block[firstNewline+1 : lastNewline]
}

func TestSatellitePreprocessingModeActionsReachSettingsAndRerenderCategory(t *testing.T) {
	preferences := &testPreferences{settings: settingsport.Snapshot{
		ContinueExisting: true, CardDetail: "standard", CardPageLimit: 64,
		ShowTechnicalActions: true, SessionLifetime: "never", QueueLimit: 32,
		VoiceRecognition: "parakeet", PreprocessingEnabled: true,
		SatellitePreprocessingMode: settingsport.SatellitePreprocessingShared,
	}}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{Settings: preferences})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	for _, item := range []struct {
		action telegramcontroller.SemanticActionKind
		mode   settingsport.SatellitePreprocessingMode
		label  string
	}{
		{telegramcontroller.SemanticSettingsPreprocessingDisabled, settingsport.SatellitePreprocessingDisabled, "Выключен"},
		{telegramcontroller.SemanticSettingsPreprocessingPerSession, settingsport.SatellitePreprocessingPerSession, "На сессию"},
		{telegramcontroller.SemanticSettingsPreprocessingShared, settingsport.SatellitePreprocessingShared, "Общий"},
	} {
		result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: item.action})
		if err != nil || result.Surface == nil || !strings.Contains(result.Surface.Text, item.label) {
			t.Fatalf("HandleSemanticAction(%q) = (%#v, %v), want preprocessing category label %q", item.action, result, err, item.label)
		}
		if preferences.settings.SatellitePreprocessingMode != item.mode {
			t.Fatalf("mode after %q = %q, want %q", item.action, preferences.settings.SatellitePreprocessingMode, item.mode)
		}
	}
}
