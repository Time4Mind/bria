package telegramapp_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/telegramapp"
	"bria/internal/telegramui"
)

func TestHandlerCreatesSessionForExactOwnerPrivateChat(t *testing.T) {
	t.Parallel()

	intent := app.ConfirmedSessionIntent{
		IntentID:   "telegram-update:101",
		ComputerID: "computer-1",
		Provider:   domain.ProviderCodex,
		Workdir:    "/workspace/project",
	}
	binding := domain.ProviderBinding{
		Provider:   domain.ProviderCodex,
		SessionID:  "provider-session-1",
		Generation: 1,
	}
	ready := mustReadySession(t, "session-1", intent, binding)
	creator := &recordingCreator{
		result: app.CreateSessionResult{Session: ready},
	}
	handler := mustHandler(t, 42, 42, creator)

	receipt, err := handler.HandleConfirmedSessionCreation(
		context.Background(),
		telegramapp.ConfirmedSessionCreation{
			UpdateID:     101,
			SenderUserID: 42,
			ChatID:       42,
			ChatKind:     telegramapp.ChatPrivate,
			Intent:       intent,
		},
	)
	if err != nil {
		t.Fatalf("HandleConfirmedSessionCreation() error = %v", err)
	}
	if creator.calls != 1 || creator.intents[0] != intent {
		t.Fatalf("creator calls/intents = (%d, %#v), want one exact intent %#v", creator.calls, creator.intents, intent)
	}
	if got, want := receipt.Disposition, telegramapp.DispositionCreatedReady; got != want {
		t.Fatalf("receipt disposition = %q, want %q", got, want)
	}
	if got, want := receipt.UpdateID, int64(101); got != want {
		t.Fatalf("receipt update id = %d, want %d", got, want)
	}
	if got, want := receipt.IntentID, intent.IntentID; got != want {
		t.Fatalf("receipt intent id = %q, want %q", got, want)
	}
	if got, want := receipt.SessionID, ready.ID(); got != want {
		t.Fatalf("receipt session id = %q, want %q", got, want)
	}
	if got, want := receipt.Status, domain.SessionReady; got != want {
		t.Fatalf("receipt status = %q, want %q", got, want)
	}
	if receipt.ProviderBinding == nil || *receipt.ProviderBinding != binding {
		t.Fatalf("receipt binding = %#v, want %#v", receipt.ProviderBinding, binding)
	}
	wantCard := telegramui.SessionCard{
		Computer: "computer-1",
		Provider: domain.ProviderCodex,
		Workdir:  "/workspace/project",
		State:    telegramui.SessionReady,
	}
	if receipt.Card == nil || *receipt.Card != wantCard {
		t.Fatalf("receipt card = %#v, want %#v", receipt.Card, wantCard)
	}
}

func TestHandlerSilentlyIgnoresEveryUnauthorizedGateMismatch(t *testing.T) {
	base := telegramapp.ConfirmedSessionCreation{
		// Deliberately invalid together with Intent: the authorization gate must
		// remain first and silent.
		UpdateID:     0,
		SenderUserID: 42,
		ChatID:       42,
		ChatKind:     telegramapp.ChatPrivate,
		// Deliberately invalid: the gate must run before intent validation.
		Intent: app.ConfirmedSessionIntent{},
	}

	tests := []struct {
		name  string
		event telegramapp.ConfirmedSessionCreation
	}{
		{name: "different numeric sender", event: withSender(base, 43)},
		{name: "different private chat", event: withChat(base, 43, telegramapp.ChatPrivate)},
		{name: "group", event: withChat(base, 42, telegramapp.ChatGroup)},
		{name: "channel", event: withChat(base, 42, telegramapp.ChatChannel)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			creator := &recordingCreator{}
			handler := mustHandler(t, 42, 42, creator)

			receipt, err := handler.HandleConfirmedSessionCreation(context.Background(), test.event)
			if err != nil {
				t.Fatalf("HandleConfirmedSessionCreation() error = %v, want silent no-op", err)
			}
			if got, want := receipt, (telegramapp.CreationReceipt{
				Disposition: telegramapp.DispositionIgnoredUnauthorized,
			}); got != want {
				t.Fatalf("receipt = %#v, want non-disclosing %#v", got, want)
			}
			if creator.calls != 0 {
				t.Fatalf("creator calls = %d, want 0", creator.calls)
			}
		})
	}
}

func TestHandlerDerivesStableIntentIDFromAuthorizedUpdateID(t *testing.T) {
	t.Parallel()

	creator := &recordingCreator{
		create: func(intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
			ready := mustReadySession(
				t,
				"session-normalized",
				intent,
				validBinding("provider-session-normalized"),
			)
			return app.CreateSessionResult{Session: ready}, nil
		},
	}
	handler := mustHandler(t, 42, 42, creator)
	firstIntent := validIntent("caller-controlled-first")
	secondIntent := validIntent("caller-controlled-second")
	secondIntent.Workdir = "/workspace/changed"

	firstReceipt, err := handler.HandleConfirmedSessionCreation(
		context.Background(),
		authorizedEvent(707, firstIntent),
	)
	if err != nil {
		t.Fatalf("first HandleConfirmedSessionCreation() error = %v", err)
	}
	secondReceipt, err := handler.HandleConfirmedSessionCreation(
		context.Background(),
		authorizedEvent(707, secondIntent),
	)
	if err != nil {
		t.Fatalf("second HandleConfirmedSessionCreation() error = %v", err)
	}

	wantIntentID := domain.IntentID("telegram-update:707")
	if got := creator.intents[0].IntentID; got != wantIntentID {
		t.Fatalf("first creator intent id = %q, want %q", got, wantIntentID)
	}
	if got := creator.intents[1].IntentID; got != wantIntentID {
		t.Fatalf("second creator intent id = %q, want %q", got, wantIntentID)
	}
	if creator.intents[0].Workdir != firstIntent.Workdir || creator.intents[1].Workdir != secondIntent.Workdir {
		t.Fatalf("creator workdirs = (%q, %q), want unchanged payloads (%q, %q)", creator.intents[0].Workdir, creator.intents[1].Workdir, firstIntent.Workdir, secondIntent.Workdir)
	}
	if firstReceipt.IntentID != wantIntentID || secondReceipt.IntentID != wantIntentID {
		t.Fatalf("receipt intent ids = (%q, %q), want (%q, %q)", firstReceipt.IntentID, secondReceipt.IntentID, wantIntentID, wantIntentID)
	}
}

func TestHandlerRejectsInvalidAuthorizedUpdateIDBeforeCreator(t *testing.T) {
	t.Parallel()

	for _, updateID := range []int64{0, -1} {
		t.Run(stringID(updateID), func(t *testing.T) {
			creator := &recordingCreator{}
			handler := mustHandler(t, 42, 42, creator)

			receipt, err := handler.HandleConfirmedSessionCreation(
				context.Background(),
				authorizedEvent(updateID, validIntent("caller-controlled")),
			)
			if err == nil {
				t.Fatal("HandleConfirmedSessionCreation() error = nil, want invalid update error")
			}
			if creator.calls != 0 {
				t.Fatalf("creator calls = %d, want 0", creator.calls)
			}
			if receipt != (telegramapp.CreationReceipt{}) {
				t.Fatalf("receipt = %#v, want zero unconfirmed receipt", receipt)
			}
		})
	}
}

func TestHandlerMapsReplayedCreation(t *testing.T) {
	t.Parallel()

	intent := validIntent("telegram-update:303")
	binding := validBinding("provider-session-replayed")
	ready := mustReadySession(t, "session-replayed", intent, binding)
	creator := &recordingCreator{result: app.CreateSessionResult{
		Session:  ready,
		Replayed: true,
	}}
	handler := mustHandler(t, 42, 42, creator)

	receipt, err := handler.HandleConfirmedSessionCreation(context.Background(), authorizedEvent(303, intent))
	if err != nil {
		t.Fatalf("HandleConfirmedSessionCreation() error = %v", err)
	}
	if got, want := receipt.Disposition, telegramapp.DispositionReplayed; got != want {
		t.Fatalf("receipt disposition = %q, want %q", got, want)
	}
	if receipt.SessionID != ready.ID() || receipt.Status != domain.SessionReady {
		t.Fatalf("receipt session/status = (%q, %q), want (%q, %q)", receipt.SessionID, receipt.Status, ready.ID(), domain.SessionReady)
	}
	if receipt.ProviderBinding == nil || *receipt.ProviderBinding != binding {
		t.Fatalf("receipt binding = %#v, want %#v", receipt.ProviderBinding, binding)
	}
	wantCard := telegramui.SessionCard{
		Computer: "computer-1",
		Provider: domain.ProviderCodex,
		Workdir:  "/workspace/project",
		State:    telegramui.SessionReady,
	}
	if receipt.Card == nil || *receipt.Card != wantCard {
		t.Fatalf("receipt card = %#v, want %#v", receipt.Card, wantCard)
	}
}

func TestHandlerMapsDurableStartFailureToAwaitingRecovery(t *testing.T) {
	t.Parallel()

	intent := validIntent("telegram-update:404")
	starting := mustStartingSession(t, "session-awaiting", intent)
	awaiting, err := starting.AwaitRecovery()
	if err != nil {
		t.Fatalf("AwaitRecovery() error = %v", err)
	}
	creator := &recordingCreator{result: app.CreateSessionResult{
		Session:    awaiting,
		StartError: errors.New("fixture child failed to start"),
	}}
	handler := mustHandler(t, 42, 42, creator)

	receipt, err := handler.HandleConfirmedSessionCreation(context.Background(), authorizedEvent(404, intent))
	if err != nil {
		t.Fatalf("HandleConfirmedSessionCreation() error = %v", err)
	}
	if got, want := receipt.Disposition, telegramapp.DispositionAwaitingRecovery; got != want {
		t.Fatalf("receipt disposition = %q, want %q", got, want)
	}
	if got, want := receipt.Status, domain.SessionAwaitingRecovery; got != want {
		t.Fatalf("receipt status = %q, want %q", got, want)
	}
	if receipt.ProviderBinding != nil {
		t.Fatalf("receipt binding = %#v, want nil", receipt.ProviderBinding)
	}
	wantCard := telegramui.SessionCard{
		Computer: "computer-1",
		Provider: domain.ProviderCodex,
		Workdir:  "/workspace/project",
		State:    telegramui.SessionAwaitingRecovery,
	}
	if receipt.Card == nil || *receipt.Card != wantCard {
		t.Fatalf("receipt card = %#v, want %#v", receipt.Card, wantCard)
	}
}

func TestHandlerRejectsInconsistentCreatorResults(t *testing.T) {
	intent := validIntent("telegram-update:505")
	starting := mustStartingSession(t, "session-starting", intent)
	ready := mustReadySession(t, "session-ready", intent, validBinding("provider-session-ready"))
	otherIntent := intent
	otherIntent.Workdir = "/workspace/different"
	mismatched := mustReadySession(t, "session-mismatched", otherIntent, validBinding("provider-session-other"))

	tests := []struct {
		name   string
		result app.CreateSessionResult
	}{
		{name: "new session remains starting", result: app.CreateSessionResult{Session: starting}},
		{name: "ready session also reports start failure", result: app.CreateSessionResult{
			Session:    ready,
			StartError: errors.New("contradictory failure"),
		}},
		{name: "persisted binding choices differ from confirmation", result: app.CreateSessionResult{Session: mismatched}},
		{name: "replay reports a new start failure", result: app.CreateSessionResult{
			Session:    ready,
			Replayed:   true,
			StartError: errors.New("contradictory replay failure"),
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			creator := &recordingCreator{result: test.result}
			handler := mustHandler(t, 42, 42, creator)

			receipt, err := handler.HandleConfirmedSessionCreation(context.Background(), authorizedEvent(505, intent))
			if !errors.Is(err, telegramapp.ErrInconsistentCreationResult) {
				t.Fatalf("HandleConfirmedSessionCreation() error = %v, want inconsistent-result error", err)
			}
			if receipt != (telegramapp.CreationReceipt{}) {
				t.Fatalf("receipt = %#v, want zero unconfirmed receipt", receipt)
			}
		})
	}
}

func TestHandlerDoesNotIssueReceiptWhenCreatorOutcomeIsUnconfirmed(t *testing.T) {
	t.Parallel()

	creationFailure := errors.New("durable outcome unknown")
	creator := &recordingCreator{err: creationFailure}
	handler := mustHandler(t, 42, 42, creator)

	receipt, err := handler.HandleConfirmedSessionCreation(
		context.Background(),
		authorizedEvent(606, validIntent("telegram-update:606")),
	)
	if !errors.Is(err, creationFailure) {
		t.Fatalf("HandleConfirmedSessionCreation() error = %v, want %v", err, creationFailure)
	}
	if receipt != (telegramapp.CreationReceipt{}) {
		t.Fatalf("receipt = %#v, want zero unconfirmed receipt", receipt)
	}
}

type recordingCreator struct {
	result  app.CreateSessionResult
	err     error
	create  func(app.ConfirmedSessionIntent) (app.CreateSessionResult, error)
	calls   int
	intents []app.ConfirmedSessionIntent
}

func (creator *recordingCreator) Create(
	_ context.Context,
	intent app.ConfirmedSessionIntent,
) (app.CreateSessionResult, error) {
	creator.calls++
	creator.intents = append(creator.intents, intent)
	if creator.create != nil {
		return creator.create(intent)
	}
	return creator.result, creator.err
}

func mustHandler(
	t *testing.T,
	ownerUserID int64,
	ownerPrivateChatID int64,
	creator telegramapp.SessionCreator,
) *telegramapp.Handler {
	t.Helper()
	handler, err := telegramapp.NewHandler(ownerUserID, ownerPrivateChatID, creator)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}

func mustReadySession(
	t *testing.T,
	id domain.SessionID,
	intent app.ConfirmedSessionIntent,
	binding domain.ProviderBinding,
) domain.Session {
	t.Helper()
	starting := mustStartingSession(t, id, intent)
	ready, err := starting.Ready(binding)
	if err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
	return ready
}

func mustStartingSession(
	t *testing.T,
	id domain.SessionID,
	intent app.ConfirmedSessionIntent,
) domain.Session {
	t.Helper()
	starting, err := domain.NewStartingSession(
		id,
		intent.IntentID,
		intent.ComputerID,
		intent.Provider,
		intent.Workdir,
	)
	if err != nil {
		t.Fatalf("NewStartingSession() error = %v", err)
	}
	return starting
}

func validIntent(id domain.IntentID) app.ConfirmedSessionIntent {
	return app.ConfirmedSessionIntent{
		IntentID:   id,
		ComputerID: "computer-1",
		Provider:   domain.ProviderCodex,
		Workdir:    "/workspace/project",
	}
}

func validBinding(id string) domain.ProviderBinding {
	return domain.ProviderBinding{
		Provider:   domain.ProviderCodex,
		SessionID:  id,
		Generation: 1,
	}
}

func authorizedEvent(updateID int64, intent app.ConfirmedSessionIntent) telegramapp.ConfirmedSessionCreation {
	return telegramapp.ConfirmedSessionCreation{
		UpdateID:     updateID,
		SenderUserID: 42,
		ChatID:       42,
		ChatKind:     telegramapp.ChatPrivate,
		Intent:       intent,
	}
}

func withSender(
	event telegramapp.ConfirmedSessionCreation,
	senderUserID int64,
) telegramapp.ConfirmedSessionCreation {
	event.SenderUserID = senderUserID
	return event
}

func withChat(
	event telegramapp.ConfirmedSessionCreation,
	chatID int64,
	kind telegramapp.ChatKind,
) telegramapp.ConfirmedSessionCreation {
	event.ChatID = chatID
	event.ChatKind = kind
	return event
}

func stringID(id int64) string {
	if id == 0 {
		return "zero"
	}
	return "negative"
}
