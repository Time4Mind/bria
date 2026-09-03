package telegramsessions_test

import (
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramsessions"
)

func session(t *testing.T, id string, status domain.SessionStatus) domain.Session {
	t.Helper()
	s, err := domain.NewStartingSession(domain.SessionID(id), domain.IntentID("intent-"+id), "mac", domain.ProviderCodex, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if status == domain.SessionReady {
		s, err = s.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-" + id, Generation: 1})
	} else if status == domain.SessionAwaitingRecovery {
		s, err = s.AwaitRecovery()
	} else if status == domain.SessionArchived || status == domain.SessionResumeFailed {
		s, err = s.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-" + id, Generation: 1})
		if err == nil {
			s, err = s.BeginClose(s.StateChangedAt().Add(1))
		}
		if err == nil {
			s, err = s.Archive(s.StateChangedAt().Add(1))
		}
		if err == nil && status == domain.SessionResumeFailed {
			s, err = s.BeginResume(s.StateChangedAt().Add(1))
		}
		if err == nil && status == domain.SessionResumeFailed {
			s, err = s.FailResume(s.StateChangedAt().Add(1))
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestItemsActiveBackgroundArchived(t *testing.T) {
	m, err := telegramsessions.New([]domain.Session{
		session(t, "z", domain.SessionReady),
		session(t, "a", domain.SessionAwaitingRecovery),
		session(t, "b", domain.SessionReady),
	}, "b")
	if err != nil {
		t.Fatal(err)
	}
	items := m.Items()
	if len(items) != 3 || items[0].Kind != telegramsessions.Active || items[0].Session.ID() != "b" ||
		items[1].Kind != telegramsessions.Background || items[1].Session.ID() != "a" ||
		items[2].Kind != telegramsessions.Background || items[2].Session.ID() != "z" {
		t.Fatalf("items = %#v", items)
	}
}

func TestSelectDoesNotStopBackgroundAndAllowsEveryOpenSession(t *testing.T) {
	m, err := telegramsessions.New([]domain.Session{session(t, "a", domain.SessionReady), session(t, "b", domain.SessionReady), session(t, "c", domain.SessionStarting), session(t, "d", domain.SessionArchived)}, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Select("b"); err != nil {
		t.Fatal(err)
	}
	if m.Active() != "b" {
		t.Fatalf("active = %q", m.Active())
	}
	if got, ok := m.Session("a"); !ok || got.Status() != domain.SessionReady {
		t.Fatal("background session was changed")
	}
	if err := m.Select("c"); err != nil || m.Active() != "c" {
		t.Fatalf("starting open session was not selectable: active=%q err=%v", m.Active(), err)
	}
	if err := m.Select("d"); err == nil {
		t.Fatal("archived session selected")
	}
}

func TestNewRejectsUnknownActiveAndDuplicateUsesLatest(t *testing.T) {
	if _, err := telegramsessions.New([]domain.Session{session(t, "a", domain.SessionReady)}, "missing"); err == nil {
		t.Fatal("unknown active accepted")
	}
	m, err := telegramsessions.New([]domain.Session{session(t, "a", domain.SessionReady), session(t, "a", domain.SessionAwaitingRecovery)}, "a")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Session("a"); got.Status() != domain.SessionAwaitingRecovery {
		t.Fatal("duplicate was not deterministic latest")
	}
}

func TestNewRequiresExactlyOneActiveWhenOpenSessionsExist(t *testing.T) {
	if _, err := telegramsessions.New([]domain.Session{session(t, "a", domain.SessionStarting)}, ""); err == nil {
		t.Fatal("New() accepted an open session list without one active session")
	}
	if _, err := telegramsessions.New([]domain.Session{session(t, "a", domain.SessionArchived)}, ""); err != nil {
		t.Fatalf("New() rejected archive-only list without active session: %v", err)
	}
}

func TestItemsClassifyTransientOpenStatesAsBackgroundAndOnlyClosedAsArchived(t *testing.T) {
	m, err := telegramsessions.New([]domain.Session{
		session(t, "a", domain.SessionStarting),
		session(t, "b", domain.SessionAwaitingRecovery),
		session(t, "c", domain.SessionArchived),
		session(t, "d", domain.SessionResumeFailed),
	}, "a")
	if err != nil {
		t.Fatal(err)
	}
	items := m.Items()
	want := map[domain.SessionID]telegramsessions.Kind{
		"a": telegramsessions.Active,
		"b": telegramsessions.Background,
		"c": telegramsessions.Archived,
		"d": telegramsessions.Archived,
	}
	for _, item := range items {
		if item.Kind != want[item.Session.ID()] {
			t.Fatalf("session %q kind = %q, want %q", item.Session.ID(), item.Kind, want[item.Session.ID()])
		}
	}
}
