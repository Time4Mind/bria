package telegramstate_test

import (
	"context"
	"path/filepath"
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramstate"
)

func validState() telegramstate.State {
	s := telegramstate.New()
	s.ActiveSession = domain.SessionID("session-1")
	s.ScreenEnabled = true
	s.Cards[s.ActiveSession] = telegramstate.Card{
		SessionID:       s.ActiveSession,
		Carrier:         telegramstate.Carrier{ChatID: 7, MessageID: 42},
		Page:            telegramstate.Page{Current: 2, Total: 4, Anchor: "event-2", FollowLatest: false},
		OptionsExpanded: true,
	}
	return s
}

func TestStateValidateRejectsBrokenCardAndPreservesClone(t *testing.T) {
	s := validState()
	if err := s.Validate(); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}
	broken := s.Clone()
	broken.Cards[s.ActiveSession] = telegramstate.Card{SessionID: s.ActiveSession, Carrier: telegramstate.Carrier{ChatID: 7}}
	if err := broken.Validate(); err == nil {
		t.Fatal("partial Telegram carrier accepted")
	}
	clone := s.Clone()
	clone.Cards[s.ActiveSession] = telegramstate.Card{SessionID: s.ActiveSession}
	if s.Cards[s.ActiveSession].Carrier.MessageID != 42 {
		t.Fatal("clone mutation changed original")
	}
}

func TestMemoryStoreUpdateIsValidatedAndDurableWithinStore(t *testing.T) {
	ctx := context.Background()
	store := telegramstate.NewMemoryStore()
	if err := store.Update(ctx, func(s *telegramstate.State) error {
		*s = validState()
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := store.Load(ctx)
	if err != nil || got.ActiveSession != domain.SessionID("session-1") || !got.ScreenEnabled {
		t.Fatalf("load = %#v, err=%v", got, err)
	}
	if err := store.Update(ctx, func(s *telegramstate.State) error {
		s.Cards[s.ActiveSession] = telegramstate.Card{SessionID: s.ActiveSession, Page: telegramstate.Page{Current: 9, Total: 1}}
		return nil
	}); err == nil {
		t.Fatal("invalid update accepted")
	}
	got, _ = store.Load(ctx)
	if got.Cards[got.ActiveSession].Page.Current != 2 {
		t.Fatal("failed update changed stored state")
	}
}

func TestFileStoreRoundTripAndMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui-state.json")
	store, err := telegramstate.OpenFileStore(path)
	if err != nil {
		t.Fatalf("open missing store: %v", err)
	}
	initial, err := store.Load(context.Background())
	if err != nil || len(initial.Cards) != 0 {
		t.Fatalf("initial = %#v, err=%v", initial, err)
	}
	if err := store.Update(context.Background(), func(s *telegramstate.State) error { *s = validState(); return nil }); err != nil {
		t.Fatalf("write: %v", err)
	}
	reopened, err := telegramstate.OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Load(context.Background())
	if err != nil || got.ActiveSession != domain.SessionID("session-1") || got.Cards[got.ActiveSession].Carrier.MessageID != 42 {
		t.Fatalf("round trip = %#v, err=%v", got, err)
	}
}
