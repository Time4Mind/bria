package nativecli

import (
	"bria/internal/domain"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExactSelectedWorkspaceTrustApprovedOnce(t *testing.T) {
	screen := "Do you trust the files in this folder?\n/root/bria\n❯ 1. Yes, I trust this folder\n  2. No, exit"
	terminal := &fakeTerminal{screens: []string{screen, screen, "Claude Code\n❯"}}
	_, err := Ready(context.Background(), terminal, Plan{Provider: domain.ProviderClaude, SessionID: testID, Workdir: "/root/bria"})
	if err != nil || !reflect.DeepEqual(terminal.keys, []string{"Enter"}) || len(terminal.inputs) != 0 {
		t.Fatal("exact workspace trust did not use one affirmative key")
	}
	negative := strings.Replace(strings.Replace(screen, "❯ 1.", "  1.", 1), "  2.", "❯ 2.", 1)
	if keys, ok := workspaceTrustKeys(negative, "/root/bria"); !ok || !reflect.DeepEqual(keys, []string{"Up", "Enter"}) {
		t.Fatal("negative selection not moved to exact affirmative")
	}
}

type repeatedTrustTerminal struct {
	enters     int
	neverReady bool
}

func (f *repeatedTrustTerminal) Capture(context.Context) (string, error) {
	if f.enters >= 2 && !f.neverReady {
		return "Claude Code\n❯", nil
	}
	return "Do you trust the files in this folder?\n/root/bria\n❯ 1. Yes, I trust this folder\n  2. No, exit", nil
}
func (f *repeatedTrustTerminal) Key(_ context.Context, key string) error {
	if key == "Enter" {
		f.enters++
	}
	return nil
}
func (f *repeatedTrustTerminal) Input(context.Context, string) error { return nil }

func TestClaudeReappearingExactTrustCanComplete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	terminal := &repeatedTrustTerminal{}
	if _, err := Ready(ctx, terminal, Plan{Provider: domain.ProviderClaude, SessionID: testID, Workdir: "/root/bria"}); err != nil || terminal.enters != 2 {
		t.Fatal("exact repeated trust was not completed", err)
	}
}

func TestClaudePersistentTrustDoesNotSendUnlimitedConfirmations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	terminal := &repeatedTrustTerminal{neverReady: true}
	_, err := Ready(ctx, terminal, Plan{Provider: domain.ProviderClaude, SessionID: testID, Workdir: "/root/bria"})
	if err == nil || err.Error() != "native CLI workspace trust confirmation required" || terminal.enters != 3 {
		t.Fatal("persistent trust not stopped with bounded classification", err)
	}
}

func TestWorkspaceTrustRejectsForeignMissingAndUnrecognizedOptions(t *testing.T) {
	screen := "Do you trust the files in this folder?\n/root/bria\n❯ 1. Yes, I trust this folder\n  2. No, exit"
	for _, bad := range []string{
		strings.Replace(screen, "/root/bria", "/foreign", 1),
		strings.Replace(screen, "/root/bria", "/root/bria-other", 1),
		strings.Replace(screen, "/root/bria", "", 1),
		strings.Replace(screen, "Yes, I trust this folder", "Yes, I trust these settings", 1),
		strings.Replace(screen, "❯", "", 1),
	} {
		if _, ok := workspaceTrustKeys(bad, "/root/bria"); ok {
			t.Fatal("ambiguous/foreign trust accepted")
		}
	}
}

func TestClaude251UnnumberedCancelFirstTrustMenu(t *testing.T) {
	screen := "Quick safety check: Is this a project you created or one you trust?\n/root/bria\n❯ No, exit\n  Yes, I trust this folder"
	keys, ok := workspaceTrustKeys(screen, "/root/bria")
	if !ok || !reflect.DeepEqual(keys, []string{"Down", "Enter"}) {
		t.Fatal("cancel-first native menu selected incorrectly")
	}
	if keys, ok := workspaceTrustKeys(strings.Replace(strings.Replace(screen, "❯ No,", "  No,", 1), "  Yes,", "❯ Yes,", 1), "/root/bria"); !ok || !reflect.DeepEqual(keys, []string{"Enter"}) {
		t.Fatal("affirmative native selection moved unnecessarily")
	}
	if _, ok := workspaceTrustKeys(strings.Replace(screen, "/root/bria", "/different", 1), "/root/bria"); ok {
		t.Fatal("foreign unnumbered trust accepted")
	}
	if _, ok := workspaceTrustKeys(strings.Replace(screen, "❯", "", 1), "/root/bria"); ok {
		t.Fatal("missing visible selection accepted")
	}
}

func TestClaudeTrustVerifiesAffirmativeAfterMovingBeforeEnter(t *testing.T) {
	no := "Quick safety check: Is this a project you created or one you trust?\n/root/bria\n❯ No, exit\n  Yes, I trust this folder\nEnter to confirm · Esc to cancel"
	yes := strings.Replace(strings.Replace(no, "❯ No,", "  No,", 1), "  Yes,", "❯ Yes,", 1)
	terminal := &fakeTerminal{screens: []string{no, yes, "Claude Code\n❯"}}
	if _, err := Ready(context.Background(), terminal, Plan{Provider: domain.ProviderClaude, SessionID: testID, Workdir: "/root/bria"}); err != nil || !reflect.DeepEqual(terminal.keys, []string{"Down", "Enter"}) {
		t.Fatal("affirmative was not verified before submit")
	}
	foreign := &fakeTerminal{screens: []string{no, strings.Replace(yes, "/root/bria", "/foreign", 1)}}
	if _, err := Ready(context.Background(), foreign, Plan{Provider: domain.ProviderClaude, SessionID: testID, Workdir: "/root/bria"}); err == nil || !reflect.DeepEqual(foreign.keys, []string{"Down"}) {
		t.Fatal("foreign/unconfirmed selection was submitted")
	}
}

func TestClaudeObservedFullWorkspaceTrustScreen(t *testing.T) {
	no := "WARNING: unknown model 'k3-256k'\n────────────────────────────────────────\nAccessing workspace:\n\n /root/bria\n\nQuick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project)\nClaude Code'll be able to read, edit, and execute files here.\nSecurity guide\n\n❯ No, exit\n  Yes, I trust this folder\n\nEnter to confirm · Esc to cancel"
	yes := strings.Replace(strings.Replace(no, "❯ No,", "  No,", 1), "  Yes,", "❯ Yes,", 1)
	terminal := &fakeTerminal{screens: []string{no, yes, "Claude Code\n❯"}}
	plan, err := BuildWithPolicy(domain.ProviderClaude, []string{"claude"}, "/root/bria", testID, LaunchPolicy{Root: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Ready(context.Background(), terminal, plan); err != nil || !reflect.DeepEqual(terminal.keys, []string{"Down", "Enter"}) {
		t.Fatal("observed full screen did not approve exact workspace", err)
	}
}
