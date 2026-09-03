package recoveryruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bria/internal/claudestore"
	"bria/internal/domain"
	"bria/internal/recoveryruntime"
	"bria/internal/sessionruntime"
)

func TestClaudeReaderCorrelatesExactCallerIDsInOriginalSessionAfterReopen(t *testing.T) {
	const sessionID = "00000000-0000-4000-8000-000000000077"
	root := t.TempDir()
	project := filepath.Join(root, "-work-project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"user","userType":"external","promptSource":"sdk","sessionId":"` + sessionID + `","cwd":"/work/project","timestamp":"2026-09-03T10:00:00Z","uuid":"telegram-update:77","parentUuid":"","message":{"role":"user","content":"same text"}}`,
		`{"type":"assistant","sessionId":"` + sessionID + `","cwd":"/work/project","timestamp":"2026-09-03T10:00:01Z","uuid":"assistant-77","parentUuid":"telegram-update:77","message":{"role":"assistant","stop_reason":"end_turn","content":[]}}`,
		`{"type":"user","userType":"external","promptSource":"sdk","sessionId":"` + sessionID + `","cwd":"/work/project","timestamp":"2026-09-03T10:01:00Z","uuid":"telegram-update:78","parentUuid":"assistant-77","message":{"role":"user","content":"same text"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(project, sessionID+".jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := claudestore.NewTranscriptStore(root, claudestore.TranscriptStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := recoveryruntime.NewClaude(store)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.ReadAcceptedTurns(context.Background(), sessionruntime.AcceptedTurnReadRequest{
		SessionID: "logical-77", Provider: domain.ProviderClaude, Workdir: "/work/project",
		Binding: domain.ProviderBinding{Provider: domain.ProviderClaude, SessionID: sessionID, Generation: 4},
	})
	if err != nil {
		t.Fatalf("ReadAcceptedTurns() error = %v", err)
	}
	want := sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{
		{MessageID: "telegram-update:77", Outcome: sessionruntime.AcceptedTurnCompleted},
		{MessageID: "telegram-update:78", Outcome: sessionruntime.AcceptedTurnUnknown},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadAcceptedTurns() = %#v, want %#v", got, want)
	}
	var _ sessionruntime.AcceptedTurnReader = reader
}

func TestClaudeReaderCorrelatesExactCallerIDsAcrossFreshOSProcess(t *testing.T) {
	const sessionID = "00000000-0000-4000-8000-000000000077"
	root := t.TempDir()
	project := filepath.Join(root, "-work-project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"user","userType":"external","promptSource":"sdk","sessionId":"` + sessionID + `","cwd":"/work/project","timestamp":"2026-09-03T10:00:00Z","uuid":"telegram-update:77","parentUuid":"","message":{"role":"user","content":"same text"}}`,
		`{"type":"assistant","sessionId":"` + sessionID + `","cwd":"/work/project","timestamp":"2026-09-03T10:00:01Z","uuid":"assistant-77","parentUuid":"telegram-update:77","message":{"role":"assistant","stop_reason":"end_turn","content":[]}}`,
		`{"type":"user","userType":"external","promptSource":"sdk","sessionId":"` + sessionID + `","cwd":"/work/project","timestamp":"2026-09-03T10:01:00Z","uuid":"telegram-update:78","parentUuid":"assistant-77","message":{"role":"user","content":"same text"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(project, sessionID+".jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestClaudeReaderFreshProcessHelper$")
	command.Env = append(os.Environ(), "BRIA_CLAUDE_RECOVERY_TEST_ROOT="+root)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("fresh recovery process error = %v", err)
	}
	var got sessionruntime.AcceptedTurnReconciliation
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode fresh recovery receipt: %v", err)
	}
	want := sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{
		{MessageID: "telegram-update:77", Outcome: sessionruntime.AcceptedTurnCompleted},
		{MessageID: "telegram-update:78", Outcome: sessionruntime.AcceptedTurnUnknown},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh recovery receipt = %#v, want %#v", got, want)
	}
}

func TestClaudeReaderFreshProcessHelper(t *testing.T) {
	root := os.Getenv("BRIA_CLAUDE_RECOVERY_TEST_ROOT")
	if root == "" {
		return
	}
	store, err := claudestore.NewTranscriptStore(root, claudestore.TranscriptStoreOptions{})
	if err != nil {
		os.Exit(51)
	}
	reader, err := recoveryruntime.NewClaude(store)
	if err != nil {
		os.Exit(52)
	}
	result, err := reader.ReadAcceptedTurns(context.Background(), sessionruntime.AcceptedTurnReadRequest{
		SessionID: "logical-77", Provider: domain.ProviderClaude, Workdir: "/work/project",
		Binding: domain.ProviderBinding{
			Provider: domain.ProviderClaude, SessionID: "00000000-0000-4000-8000-000000000077", Generation: 4,
		},
	})
	if err != nil {
		os.Exit(53)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		os.Exit(54)
	}
	os.Exit(0)
}

func TestClaudeReaderFailsClosedForStaleBinding(t *testing.T) {
	store, err := claudestore.NewTranscriptStore(t.TempDir(), claudestore.TranscriptStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := recoveryruntime.NewClaude(store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadAcceptedTurns(context.Background(), sessionruntime.AcceptedTurnReadRequest{
		SessionID: "logical-77", Provider: domain.ProviderCodex, Workdir: "/work/project",
		Binding: domain.ProviderBinding{Provider: domain.ProviderClaude, SessionID: "00000000-0000-4000-8000-000000000077", Generation: 4},
	})
	if !errors.Is(err, recoveryruntime.ErrUnavailable) {
		t.Fatalf("ReadAcceptedTurns() error = %v, want ErrUnavailable", err)
	}
}
