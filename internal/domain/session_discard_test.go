package domain_test

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestDiscardSessionIsNeverVisibleInArchiveAndCompletionRemovesArtifacts(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "empty", "alpha", 1, time.Unix(10, 0).UTC())
	if err := state.ShareSession(1, ref, 2, domain.ShareView); err != nil {
		t.Fatal(err)
	}
	state.Navigation.SessionActivityByUser[1] = map[string]time.Time{
		ref.Key(): time.Unix(11, 0).UTC(),
	}
	state.TelegramSessionViews[1] = map[string]domain.TelegramSessionView{
		ref.Key(): {Page: 1, Pages: 1, Follow: true},
	}
	session := state.Sessions[ref.Key()]
	state.TelegramResponseCards[1] = domain.TelegramResponseCard{
		ChatID: 1, MessageID: 9, Session: ref, SessionRevision: session.Revision,
		SessionEventAt: session.LastEventAt,
	}

	if err := state.DiscardSession(
		1, ref, session.Revision, time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	discarding := state.Sessions[ref.Key()]
	if discarding.State != domain.SessionDiscarding || !discarding.ArchivedAt.IsZero() ||
		discarding.DiscardedAt.IsZero() {
		t.Fatalf("discarding session=%#v", discarding)
	}
	if got := state.VisibleSessions(1, true); len(got) != 0 {
		t.Fatalf("discarding session remained live: %#v", got)
	}
	if got := state.VisibleSessions(1, false); len(got) != 0 {
		t.Fatalf("discarding session leaked into archive: %#v", got)
	}
	if err := state.CompleteSessionDiscard(1, ref, discarding.Revision); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Sessions[ref.Key()]; ok {
		t.Fatal("discarded session record remained")
	}
	for _, grant := range state.Grants {
		if grant.Session == ref {
			t.Fatalf("discarded grant remained: %#v", grant)
		}
	}
	if _, ok := state.Navigation.SessionActivityByUser[1][ref.Key()]; ok {
		t.Fatal("discarded activity remained")
	}
	if _, ok := state.TelegramSessionViews[1][ref.Key()]; ok {
		t.Fatal("discarded Telegram view remained")
	}
	card := state.TelegramResponseCards[1]
	if card.MessageID != 9 || card.Session != (domain.SessionRef{}) || card.SessionRevision != 0 {
		t.Fatalf("response carrier cleanup=%#v", card)
	}
}
