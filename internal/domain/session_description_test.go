package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestArchiveDescriptionIsStoredOnceAndClearedOnRestore(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "described", "alpha", 1, time.Unix(10, 0).UTC())
	session := state.Sessions[ref.Key()]
	if err := state.CloseSession(
		1, ref, session.Revision, "archive-described", time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if err := state.ObserveArchiveInventory(
		"alpha", []string{"archive-described"}, time.Unix(21, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	closed := state.Sessions[ref.Key()]
	if err := state.SetArchiveDescription(
		ref, closed.Revision, closed.ArchiveID,
		[]string{"  Первый   запрос. ", "Нужный результат."},
		domain.ArchiveDescriptionVersion,
	); err != nil {
		t.Fatal(err)
	}
	described := state.Sessions[ref.Key()]
	if got := described.ArchiveDescription; len(got) != 2 || got[0] != "Первый запрос." {
		t.Fatalf("description=%q", got)
	}
	if described.DescriptionVersion != domain.ArchiveDescriptionVersion ||
		described.Revision != closed.Revision+1 {
		t.Fatalf("described session=%#v", described)
	}
	if err := state.SetArchiveDescription(
		ref, described.Revision, described.ArchiveID,
		[]string{"Другая строка.", "Другой результат."}, domain.ArchiveDescriptionVersion,
	); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("overwrite error=%v", err)
	}
	if err := state.RestoreSession(1, ref, described.Revision, time.Unix(30, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	restored := state.Sessions[ref.Key()]
	if restored.ArchiveDescription != nil || restored.DescriptionVersion != 0 {
		t.Fatalf("restored description=%q version=%d", restored.ArchiveDescription, restored.DescriptionVersion)
	}
}

func TestArchiveDescriptionValidationIsBounded(t *testing.T) {
	valid := strings.Repeat("я", domain.MaxArchiveDescriptionRunes)
	if _, err := domain.NormalizeArchiveDescription([]string{valid, "Результат."}); err != nil {
		t.Fatal(err)
	}
	for _, lines := range [][]string{
		{"only one"},
		{"", "result"},
		{strings.Repeat("я", domain.MaxArchiveDescriptionRunes+1), "result"},
		{"line\x00", "result"},
	} {
		if _, err := domain.NormalizeArchiveDescription(lines); err == nil {
			t.Fatalf("accepted invalid description=%q", lines)
		}
	}
}

func TestAutomaticArchiveWithoutNativeBundleCanStoreDescription(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "automatic", "alpha", 1, time.Unix(10, 0).UTC())
	session := state.Sessions[ref.Key()]
	if err := state.ArchiveSession(
		ref, session.Revision, domain.ArchiveResumeFailed, time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	archived := state.Sessions[ref.Key()]
	if archived.ArchiveID != "" || archived.ArchiveReady {
		t.Fatalf("automatic archive=%#v", archived)
	}
	if err := state.SetArchiveDescription(
		ref, archived.Revision, "", []string{"Контекст.", "Результат."},
		domain.ArchiveDescriptionVersion,
	); err != nil {
		t.Fatal(err)
	}
}
