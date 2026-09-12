package cardactivity_test

import (
	"os"
	"path/filepath"
	"testing"

	"bria/internal/cardactivity"
	"bria/internal/domain"
	"bria/internal/telegramstate"
)

func TestSaveDoesNotReplaceIdenticalSecureSidecar(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := &telegramstate.State{Cards: map[domain.SessionID]telegramstate.Card{
		"session-a": {SessionID: "session-a", LastEventUnixNano: 42},
	}}
	if err := cardactivity.Save(statePath, state); err != nil {
		t.Fatal(err)
	}
	path := statePath + ".card-activity.json"
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cardactivity.Save(statePath, state); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("identical card activity sidecar was atomically replaced")
	}
}

func TestSaveReplacesChangedActivity(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := &telegramstate.State{Cards: map[domain.SessionID]telegramstate.Card{
		"session-a": {SessionID: "session-a", LastEventUnixNano: 42},
	}}
	if err := cardactivity.Save(statePath, state); err != nil {
		t.Fatal(err)
	}
	path := statePath + ".card-activity.json"
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	card := state.Cards["session-a"]
	card.LastEventUnixNano++
	state.Cards["session-a"] = card
	if err := cardactivity.Save(statePath, state); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("changed card activity sidecar was not replaced")
	}
}

func TestSaveRepairsPermissionsEvenWhenContentIsIdentical(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := &telegramstate.State{Cards: map[domain.SessionID]telegramstate.Card{
		"session-a": {SessionID: "session-a", LastEventUnixNano: 42},
	}}
	if err := cardactivity.Save(statePath, state); err != nil {
		t.Fatal(err)
	}
	path := statePath + ".card-activity.json"
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cardactivity.Save(statePath, state); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) || after.Mode().Perm() != 0o600 {
		t.Fatalf("insecure sidecar was not replaced: same=%t mode=%#o", os.SameFile(before, after), after.Mode().Perm())
	}
}
