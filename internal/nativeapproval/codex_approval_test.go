package nativeapproval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func approvalFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/codex-command-approval.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCodexAllObservedApprovalFixtures(t *testing.T) {
	paths, err := filepath.Glob("testdata/codex-command-approval*.txt")
	if err != nil || len(paths) != 7 {
		t.Fatal("missing observed cases", err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			r, ok := ParseCodexCommandApproval(string(b))
			if strings.Contains(path, "collapsed") {
				if !ok || !r.Incomplete {
					t.Fatal("collapsed command approval missing incomplete marker")
				}
				return
			}
			if !ok || r.OptionCount < 2 || r.OptionCount > 3 {
				t.Fatal("observed approval missing", r, ok)
			}
			if filepath.Base(path) == "codex-command-approval-wiki.txt" && !strings.Contains(r.Reason, "live-подтверждения?") {
				t.Fatal("wrapped reason lost")
			}
		})
	}
}

func TestCollapsedCommandApprovalCanBeAcceptedWithoutExpansion(t *testing.T) {
	b, err := os.ReadFile("testdata/codex-command-approval-collapsed.txt")
	if err != nil {
		t.Fatal(err)
	}
	r, ok := ParseCodexCommandApproval(string(b))
	if !ok {
		t.Fatal("recognized command dialog is blocked only because preview is collapsed")
	}
	terminal := &approvalTerminal{screen: string(b)}
	if err := AcceptCodexCommandOnce(context.Background(), terminal, r.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if strings.Join(terminal.keys, ",") != "Enter" {
		t.Fatal("collapsed dialog not accepted once", terminal.keys)
	}
}

type approvalTerminal struct {
	screen string
	keys   []string
}

func (f *approvalTerminal) Capture(context.Context) (string, error) { return f.screen, nil }
func (f *approvalTerminal) Input(context.Context, string) error     { panic("no prompt injection") }
func (f *approvalTerminal) Key(_ context.Context, key string) error {
	f.keys = append(f.keys, key)
	f.screen = "✔ You approved codex to run the command this time\n• Running command\n• Working (1s • esc to interrupt)\n› Ask Codex to do anything"
	return nil
}

func TestCodexApprovalKeepsWhitespaceInAuthorizationIdentity(t *testing.T) {
	original := approvalFixture(t)
	modified := strings.Replace(original, "\n  t=", "\n    t=", 1)
	if original == modified {
		t.Fatal("fixture no longer contains the command indentation anchor")
	}
	a, _ := ParseCodexCommandApproval(original)
	b, _ := ParseCodexCommandApproval(modified)
	if a.Fingerprint == b.Fingerprint {
		t.Fatal("different command indentation shares authorization")
	}
}

func TestCodexApprovalTwoOptionObservedFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/codex-command-approval-two-options.txt")
	if err != nil {
		t.Fatal(err)
	}
	request, ok := ParseCodexCommandApproval(string(b))
	if !ok || request.Environment != "local" || !strings.Contains(request.Preview, "yt.exists(p)") {
		t.Fatal("observed two-option menu not recognized")
	}
}

type unclearApprovalTerminal struct{ approvalTerminal }

func (f *unclearApprovalTerminal) Key(_ context.Context, key string) error {
	f.keys = append(f.keys, key)
	f.screen = ""
	return nil
}
func TestCodexApprovalBlankRerenderIsNotReceipt(t *testing.T) {
	fixture := approvalFixture(t)
	request, _ := ParseCodexCommandApproval(fixture)
	terminal := &unclearApprovalTerminal{approvalTerminal{screen: fixture}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := AcceptCodexCommandOnce(ctx, terminal, request.Fingerprint); err == nil {
		t.Fatal("blank frame reported confirmed")
	}
}

func TestCodexObservedCommandApprovalIsAcceptedOnce(t *testing.T) {
	screen := approvalFixture(t)
	request, ok := ParseCodexCommandApproval(screen)
	if !ok || request.Environment != "local" || !strings.Contains(request.Preview, `get_issue("EXAMPLEPRJX-1001")`) || len(request.Fingerprint) != 64 {
		t.Fatalf("real command approval not parsed: %+v, %v", request, ok)
	}
	terminal := &approvalTerminal{screen: screen}
	if err := AcceptCodexCommandOnce(context.Background(), terminal, request.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if strings.Join(terminal.keys, ",") != "Enter" {
		t.Fatalf("wrong one-shot decision: %v", terminal.keys)
	}
	if err := AcceptCodexCommandOnce(context.Background(), terminal, request.Fingerprint); err == nil {
		t.Fatal("replayed approval accepted")
	}
	if len(terminal.keys) != 1 {
		t.Fatal("stale screen caused extra keystroke")
	}
}

func TestCodexApprovalRejectsHistoricalUnknownOrTruncatedMenus(t *testing.T) {
	fixture := approvalFixture(t)
	for name, screen := range map[string]string{
		"old menu":            fixture + "\n› Ask Codex to do anything\ngpt-5.6-luna medium · /work",
		"different picker":    strings.Replace(fixture, "Would you like to run the following command?", "Select Model and Effort", 1),
		"persistent selected": strings.Replace(strings.Replace(fixture, "› 1.", "  1.", 1), "  2.", "› 2.", 1),
		"foreign environment": strings.Replace(fixture, "Environment: local", "Environment: production", 1),
		"no footer":           strings.Replace(fixture, "Press enter to confirm or esc to cancel", "", 1),
		"unknown option":      strings.Replace(fixture, "3. No, and tell Codex what to do differently (esc)", "3. Allow everything\n4. No, and tell Codex what to do differently (esc)", 1),
		"question":            "Choose an approach\n› 1. Yes, proceed (y)\n2. No\nPress enter to confirm or esc to cancel",
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := ParseCodexCommandApproval(screen); ok {
				t.Fatal("non-actionable screen recognized")
			}
		})
	}
}

func TestCodexApprovalRequiresExactAuthorizedFingerprint(t *testing.T) {
	fixture := approvalFixture(t)
	request, _ := ParseCodexCommandApproval(fixture)
	terminal := &approvalTerminal{screen: strings.ReplaceAll(fixture, "EXAMPLEPRJX-1001", "EXAMPLEPRJX-1002")}
	if err := AcceptCodexCommandOnce(context.Background(), terminal, request.Fingerprint); err == nil || len(terminal.keys) != 0 {
		t.Fatal("changed target approved")
	}
	terminal.screen = fixture
	if err := AcceptCodexCommandOnce(context.Background(), terminal, ""); err == nil || len(terminal.keys) != 0 {
		t.Fatal("missing authorization approved")
	}
}

type changingApprovalTerminal struct {
	approvalTerminal
	captures int
}

func (f *changingApprovalTerminal) Capture(context.Context) (string, error) {
	f.captures++
	if f.captures > 1 {
		return "› Ask Codex to do anything", nil
	}
	return f.screen, nil
}
func TestCodexApprovalRereadsBeforeInput(t *testing.T) {
	fixture := approvalFixture(t)
	request, _ := ParseCodexCommandApproval(fixture)
	terminal := &changingApprovalTerminal{approvalTerminal: approvalTerminal{screen: fixture}}
	if err := AcceptCodexCommandOnce(context.Background(), terminal, request.Fingerprint); err == nil || len(terminal.keys) != 0 {
		t.Fatal("disappeared request received Enter")
	}
}
