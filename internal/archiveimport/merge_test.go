package archiveimport_test

import (
	"errors"
	"testing"
	"time"

	"bria/internal/archiveimport"
	"bria/internal/domain"
)

func TestMergeIsIdempotentAndRejectsDuplicateProviderIdentity(t *testing.T) {
	first := archived(t, "11111111-1111-8111-8111-111111111111", "discovered:first", "provider-original")
	next, changed, err := archiveimport.Merge(nil, []domain.Session{first})
	if err != nil || !changed || !next[first.IntentID()].Equal(first) {
		t.Fatalf("Merge(new) = (%#v, %t, %v)", next, changed, err)
	}
	replayed, changed, err := archiveimport.Merge(next, []domain.Session{first})
	if err != nil || changed || !archiveimport.Equal(replayed, next) {
		t.Fatalf("Merge(replay) = (%#v, %t, %v), want unchanged", replayed, changed, err)
	}
	duplicateProvider := archived(t, "22222222-2222-8222-8222-222222222222", "discovered:second", "provider-original")
	if _, _, err := archiveimport.Merge(next, []domain.Session{duplicateProvider}); !errors.Is(err, archiveimport.ErrConflict) {
		t.Fatalf("Merge(duplicate provider identity) error = %v, want ErrConflict", err)
	}
	if !next[first.IntentID()].Equal(first) {
		t.Fatal("failed Merge mutated caller state")
	}
}

func archived(t *testing.T, id, intentID, providerSessionID string) domain.Session {
	t.Helper()
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	session, err := domain.RestoreSession(domain.SessionSnapshot{
		ID: domain.SessionID(id), IntentID: domain.IntentID(intentID), ComputerID: "macbook",
		Provider: domain.ProviderCodex, Workdir: "/work", Status: domain.SessionArchived,
		Binding:   &domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: providerSessionID, Generation: 1},
		CreatedAt: now, StateChangedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}
