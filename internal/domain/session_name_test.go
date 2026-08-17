package domain_test

import (
	"errors"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestSessionNameContract(t *testing.T) {
	valid := []string{"one", "two words", "две строки"}
	for _, input := range valid {
		name, err := domain.NormalizeSessionName(input)
		if err != nil || utf8.RuneCountInString(name) > domain.MaxSessionNameRunes {
			t.Fatalf("valid name %q became %q: %v", input, name, err)
		}
	}
	invalid := []string{"", "three word name", "thirteenchars", "line\nbreak"}
	for _, input := range invalid {
		if _, err := domain.NormalizeSessionName(input); !errors.Is(err, domain.ErrInvalidState) {
			t.Fatalf("invalid name %q error=%v", input, err)
		}
	}
}

func TestSessionNamesAreUniquePerOwner(t *testing.T) {
	state := fixtureState(t)
	first := domain.Session{
		ID: "first", NodeID: "alpha", OwnerID: 1, Name: "same name",
		NameFormatVersion: domain.SessionNameFormatVersion,
		Workdir:           "/srv/first", Backend: "codex", State: domain.SessionLive,
		CreatedAt: time.Unix(1, 0), LiveSinceAt: time.Unix(1, 0),
	}
	if err := state.AddSession(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "second"
	second.NodeID = "beta"
	second.Workdir = "/srv/second"
	if err := state.AddSession(second); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate name error=%v", err)
	}

	name, err := state.AvailableSessionName(1, second.Ref(), "same name")
	if err != nil || name != "same name-2" || utf8.RuneCountInString(name) > 12 {
		t.Fatalf("unique name=%q err=%v", name, err)
	}
}
