package nativecli

import (
	"bria/internal/domain"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testID = "12345678-1234-4234-9234-123456789abc"

func TestBuildNativeTransports(t *testing.T) {
	plan, err := Build(domain.ProviderCodex, []string{"codex", "app-server", "--stdio", "-m", "gpt-test", "--sandbox", "read-only"}, "/work", testID)
	want := []string{"codex", "-m", "gpt-test", "--ask-for-approval", "on-request", "--sandbox", "workspace-write", "--no-alt-screen", "--cd", "/work", "resume", testID}
	if err != nil || !reflect.DeepEqual(plan.Command, want) || plan.SessionID != testID {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	plan, err = Build(domain.ProviderClaude, []string{"claude", "-p", "--input-format", "stream-json", "--output-format=stream-json", "--model", "sonnet", "--permission-mode", "plan"}, "/work", "")
	if err != nil || !uuidPattern.MatchString(plan.SessionID) || !reflect.DeepEqual(plan.Command, []string{"claude", "--model", "sonnet", "--dangerously-skip-permissions", "--session-id", plan.SessionID}) {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	second, err := Build(domain.ProviderClaude, []string{"claude"}, "/work", "")
	if err != nil || second.SessionID == plan.SessionID {
		t.Fatal("new sessions must differ")
	}
	resumed, err := Build(domain.ProviderClaude, []string{"claude"}, "/work", testID)
	if err != nil || !reflect.DeepEqual(resumed.Command, []string{"claude", "--dangerously-skip-permissions", "--resume", testID}) {
		t.Fatalf("resume=%#v err=%v", resumed, err)
	}
}

func TestBuildRejectsAmbiguousLaunch(t *testing.T) {
	for _, args := range [][]string{{}, {"codex", "exec"}, {"codex", "a prompt"}, {"codex", "--unknown"}, {"codex", "--model"}} {
		if _, err := Build(domain.ProviderCodex, args, "/work", ""); err == nil {
			t.Fatalf("accepted %#v", args)
		}
	}
	if _, err := Build(domain.ProviderCodex, []string{"codex"}, "relative", ""); err == nil {
		t.Fatal("relative workdir")
	}
	if _, err := Build(domain.ProviderCodex, []string{"codex"}, "/work", "latest"); err == nil {
		t.Fatal("non-exact resume")
	}
}

type fakeTerminal struct {
	screens      []string
	captures     int
	inputs, keys []string
	err          error
}

func (f *fakeTerminal) Capture(context.Context) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	i := f.captures
	if i >= len(f.screens) {
		i = len(f.screens) - 1
	}
	f.captures++
	return f.screens[i], nil
}
func (f *fakeTerminal) Input(_ context.Context, s string) error {
	f.inputs = append(f.inputs, s)
	return nil
}
func (f *fakeTerminal) Key(_ context.Context, s string) error { f.keys = append(f.keys, s); return nil }

func TestCodexReadyUsesExactNativeStatus(t *testing.T) {
	for _, resume := range []string{"", testID} {
		terminal := &fakeTerminal{screens: []string{"OpenAI Codex\n› Ask anything\n? for shortcuts", "│ Model: gpt-test (high) │\n│ Session: " + testID + " │"}}
		state, err := Ready(context.Background(), terminal, Plan{Provider: domain.ProviderCodex, SessionID: resume})
		if err != nil || state.SessionID != testID || state.Model != "gpt-test" || !state.Ready || !reflect.DeepEqual(terminal.inputs, []string{"/status"}) || !reflect.DeepEqual(terminal.keys, []string{"Escape"}) {
			t.Fatalf("state=%#v terminal=%#v err=%v", state, terminal, err)
		}
	}
}

func TestReadyFailsClosed(t *testing.T) {
	for _, screen := range []string{"Please log in\n❯", "Do you trust the files in this folder?\n❯ 1. Yes"} {
		terminal := &fakeTerminal{screens: []string{screen}}
		if _, err := Ready(context.Background(), terminal, Plan{Provider: domain.ProviderClaude, SessionID: testID}); err == nil || len(terminal.inputs) != 0 || len(terminal.keys) != 0 {
			t.Fatal("startup gate accepted")
		}
	}
	terminal := &fakeTerminal{screens: []string{"›", "Session: 99999999-1234-4234-9234-123456789abc"}}
	if _, err := Ready(context.Background(), terminal, Plan{Provider: domain.ProviderCodex, SessionID: testID}); err == nil || !strings.Contains(err.Error(), "different session") {
		t.Fatalf("identity mismatch err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Ready(ctx, &fakeTerminal{screens: []string{"loading"}}, Plan{Provider: domain.ProviderCodex}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}

func TestClaudeReadyAndInteractiveScreen(t *testing.T) {
	state, err := Ready(context.Background(), &fakeTerminal{screens: []string{"Claude Code\n❯"}}, Plan{Provider: domain.ProviderClaude, SessionID: testID})
	if err != nil || state.SessionID != testID || !state.Ready {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	state = ParseScreen("Select a model\n❯ 1. Sonnet\nEnter to select · Esc to cancel")
	if !state.Interactive || state.Ready {
		t.Fatalf("state=%#v", state)
	}
}
