package telegramcontroller_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

type knownEmptySessions struct{ *memorySessions }

func (store knownEmptySessions) HasEmptyCloseEligibility(context.Context, domain.SessionID) (bool, error) {
	return true, nil
}

func TestEmptyCloseConfirmationAndDeletedResultReturnToSessions(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider-empty", 1)
	store := knownEmptySessions{&memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}, listed: []domain.Session{ready}}}
	closer := sessionCloserFunc(func(_ context.Context, id domain.SessionID) (app.CloseSessionResult, error) {
		closing, err := ready.BeginClose(time.Now().UTC())
		delete(store.byID, id)
		store.listed = nil
		return app.CloseSessionResult{Session: closing, Deleted: true}, err
	})
	controller := newController(t, nil, store, nil, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, SessionCloser: closer})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	confirmation, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticClose, SessionID: ready.ID()})
	if err != nil || confirmation.Card == nil || !strings.Contains(confirmation.Card.Header, "Удалить пустую сессию") {
		t.Fatalf("empty close warning = %#v, %v", confirmation.Card, err)
	}
	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticClose, SessionID: ready.ID(), Choice: 1})
	if err != nil || result.Surface == nil || result.Surface.Text != "Сессии" || result.Card != nil {
		t.Fatalf("deleted close = %#v, %v", result, err)
	}
}

type rejectedPromptUI struct{ projectionUIState }

func (state *rejectedPromptUI) SetCardPrompt(context.Context, domain.SessionID, string, string) error {
	return errors.New("session is already closing")
}

func TestPromptPersistenceFailureStopsBeforeMediaProcessing(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider-empty", 1)
	prepared := false
	controller := newController(t, nil, newLockedSessions(ready), nil, nil, telegramcontroller.Options{
		Recovered: []domain.Session{ready}, UIState: &rejectedPromptUI{},
		InputPreparer: inputPreparerFunc(func(context.Context, telegramcontroller.IncomingInput) (string, error) {
			prepared = true
			return "voice text", nil
		}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	update := message(91, "")
	update.MediaKind, update.MediaFileID, update.MediaDownloadAllowed = "voice", "file", true
	_, _ = controller.Handle(context.Background(), update)
	if prepared {
		t.Fatal("media processed after durable prompt evidence failed")
	}
}
