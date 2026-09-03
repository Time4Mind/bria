package claudestore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"bria/internal/claudestore"
)

const transcriptSessionID = "00000000-0000-4000-8000-000000000077"

func TestTranscriptStoreCorrelatesExactMessageIDsAfterFreshProcessRestart(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, transcriptSessionID, []string{
		transcriptUser("telegram-update:77", "", "same private text", "2026-09-03T10:00:00Z"),
		transcriptAssistant("assistant-77", "telegram-update:77", "end_turn", "2026-09-03T10:00:01Z"),
		transcriptUser("telegram-update:78", "assistant-77", "same private text", "2026-09-03T10:01:00Z"),
	})

	// A newly constructed store models a fresh Bria process. Correlation comes
	// only from Claude's persisted uuid and parentUuid fields, never prompt text.
	store, err := claudestore.NewTranscriptStore(root, claudestore.TranscriptStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.AcceptedTurns(context.Background(), transcriptSessionID, "/work/project")
	if err != nil {
		t.Fatalf("AcceptedTurns() error = %v", err)
	}
	want := []claudestore.AcceptedTurn{
		{MessageID: "telegram-update:77", Outcome: claudestore.AcceptedTurnCompleted},
		{MessageID: "telegram-update:78", Outcome: claudestore.AcceptedTurnUnknown},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AcceptedTurns() = %#v, want %#v", got, want)
	}
}

func TestTranscriptStoreUsesParentGraphNotTextOrFileOrder(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, transcriptSessionID, []string{
		transcriptUser("telegram-update:first", "", "identical", "2026-09-03T10:00:00Z"),
		transcriptUser("telegram-update:second", "assistant-first", "identical", "2026-09-03T10:00:02Z"),
		// The completed descendant is deliberately after the second same-text
		// input in file order but remains linked only to the first input.
		transcriptAssistant("assistant-first", "telegram-update:first", "end_turn", "2026-09-03T10:00:01Z"),
	})
	store, err := claudestore.NewTranscriptStore(root, claudestore.TranscriptStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.AcceptedTurns(context.Background(), transcriptSessionID, "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	want := []claudestore.AcceptedTurn{
		{MessageID: "telegram-update:first", Outcome: claudestore.AcceptedTurnCompleted},
		{MessageID: "telegram-update:second", Outcome: claudestore.AcceptedTurnUnknown},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AcceptedTurns() = %#v, want graph-correlated %#v", got, want)
	}
}

func TestTranscriptStoreRejectsDuplicateIdentityWithoutLeakingContentOrPath(t *testing.T) {
	root := t.TempDir()
	const privateText = "must-never-enter-an-error"
	writeTranscript(t, root, transcriptSessionID, []string{
		transcriptUser("telegram-update:duplicate", "", privateText, "2026-09-03T10:00:00Z"),
		transcriptUser("telegram-update:duplicate", "", privateText, "2026-09-03T10:01:00Z"),
	})
	store, err := claudestore.NewTranscriptStore(root, claudestore.TranscriptStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AcceptedTurns(context.Background(), transcriptSessionID, "/work/project")
	if !errors.Is(err, claudestore.ErrTranscriptUnverifiable) {
		t.Fatalf("AcceptedTurns() error = %v, want ErrTranscriptUnverifiable", err)
	}
	for _, secret := range []string{privateText, root, transcriptSessionID} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("sanitized error leaked %q: %q", secret, err)
		}
	}
}

func TestTranscriptStoreRejectsAmbiguousOriginalSessionAcrossProjects(t *testing.T) {
	root := t.TempDir()
	line := transcriptUser("telegram-update:77", "", "private", "2026-09-03T10:00:00Z")
	writeTranscriptInProject(t, root, "-work-project", transcriptSessionID, []string{line})
	writeTranscriptInProject(t, root, "-other-project", transcriptSessionID, []string{line})
	store, err := claudestore.NewTranscriptStore(root, claudestore.TranscriptStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptedTurns(context.Background(), transcriptSessionID, "/work/project"); !errors.Is(err, claudestore.ErrTranscriptUnverifiable) {
		t.Fatalf("AcceptedTurns() error = %v, want ambiguous session rejected", err)
	}
	if _, err := store.List(context.Background()); !errors.Is(err, claudestore.ErrTranscriptUnverifiable) {
		t.Fatalf("List() error = %v, want ambiguous session rejected", err)
	}
}

func TestTranscriptStoreRejectsCausallyImpossibleTerminalGraph(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, transcriptSessionID, []string{
		transcriptUser("telegram-update:77", "", "private", "2026-09-03T10:00:02Z"),
		transcriptAssistant("assistant-77", "telegram-update:77", "end_turn", "2026-09-03T10:00:01Z"),
	})
	store, err := claudestore.NewTranscriptStore(root, claudestore.TranscriptStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptedTurns(context.Background(), transcriptSessionID, "/work/project"); !errors.Is(err, claudestore.ErrTranscriptUnverifiable) {
		t.Fatalf("AcceptedTurns() error = %v, want impossible ancestry rejected", err)
	}
}

func TestTranscriptStoreRejectsDuplicateJSONIdentityKey(t *testing.T) {
	root := t.TempDir()
	line := transcriptUser("telegram-update:77", "", "private", "2026-09-03T10:00:00Z")
	line = strings.Replace(line, `"uuid":"telegram-update:77"`, `"uuid":"telegram-update:77","uuid":"telegram-update:other"`, 1)
	writeTranscript(t, root, transcriptSessionID, []string{line})
	store, err := claudestore.NewTranscriptStore(root, claudestore.TranscriptStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptedTurns(context.Background(), transcriptSessionID, "/work/project"); !errors.Is(err, claudestore.ErrTranscriptUnverifiable) {
		t.Fatalf("AcceptedTurns() error = %v, want duplicate JSON key rejected", err)
	}
}

func TestTranscriptStoreListsBoundedStructuralSessionSummary(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, transcriptSessionID, []string{
		transcriptUser("telegram-update:77", "", "private", "2026-09-03T10:00:00Z"),
		transcriptAssistant("assistant-77", "telegram-update:77", "end_turn", "2026-09-03T10:05:00Z"),
	})
	store, err := claudestore.NewTranscriptStore(root, claudestore.TranscriptStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []claudestore.SessionSummary{{
		ID: transcriptSessionID, Cwd: "/work/project",
		CreatedAt: time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 9, 3, 10, 5, 0, 0, time.UTC),
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}
}

func transcriptUser(uuid, parent, content, timestamp string) string {
	return `{"type":"user","userType":"external","promptSource":"sdk","sessionId":"` + transcriptSessionID +
		`","cwd":"/work/project","timestamp":"` + timestamp + `","uuid":"` + uuid + `","parentUuid":"` + parent +
		`","message":{"role":"user","content":` + quoteJSON(content) + `}}`
}

func transcriptAssistant(uuid, parent, stopReason, timestamp string) string {
	return `{"type":"assistant","sessionId":"` + transcriptSessionID + `","cwd":"/work/project","timestamp":"` + timestamp +
		`","uuid":"` + uuid + `","parentUuid":"` + parent + `","message":{"role":"assistant","stop_reason":"` + stopReason +
		`","content":[{"type":"text","text":"private result"}]}}`
}

func quoteJSON(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func writeTranscript(t *testing.T, root, sessionID string, lines []string) {
	t.Helper()
	writeTranscriptInProject(t, root, "-work-project", sessionID, lines)
}

func writeTranscriptInProject(t *testing.T, root, projectName, sessionID string, lines []string) {
	t.Helper()
	project := filepath.Join(root, projectName)
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, sessionID+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
