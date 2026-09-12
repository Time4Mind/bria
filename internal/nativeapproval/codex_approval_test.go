package nativeapproval

import (
	"context"
	"errors"
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
	if err != nil || len(paths) != 8 {
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

func TestCodexFileEditApprovalIsParsedForOneShotAutoApproval(t *testing.T) {
	body, err := os.ReadFile("testdata/codex-file-edits-approval.txt")
	if err != nil {
		t.Fatal(err)
	}
	request, ok := ParseCodexCommandApproval(string(body))
	if !ok {
		t.Fatal("file-edit approval was not parsed")
	}
	if request.Environment != "local" || request.OptionCount != 3 || request.Incomplete ||
		!strings.Contains(request.Preview, "Description: Apply proposed file edits") ||
		!strings.Contains(request.Preview, "Destination: /work/project/AGENTS.md") || len(request.Fingerprint) != 64 || len(request.DecisionID) != 64 {
		t.Fatalf("file-edit approval = %#v", request)
	}
	terminal := &approvalTerminal{screen: string(body), receipt: "✔ You approved codex to make the edits this time"}
	if err := AcceptCodexCommandOnce(context.Background(), terminal, request.Fingerprint); err != nil {
		t.Fatalf("file-edit approval was not accepted once: %v", err)
	}
	if strings.Join(terminal.keys, ",") != "Enter" {
		t.Fatalf("file-edit approval keys = %v", terminal.keys)
	}
}

func TestCodexApprovalParsesVisibleTailWhenHeadingExceedsTerminalWindow(t *testing.T) {
	fileBody, err := os.ReadFile("testdata/codex-file-edits-approval.txt")
	if err != nil {
		t.Fatal(err)
	}
	fileTail := string(fileBody[strings.Index(string(fileBody), "  Destination:"):])
	fileRequest, ok := ParseCodexCommandApproval(fileTail)
	if !ok || !fileRequest.Incomplete || !strings.Contains(fileRequest.Preview, "/work/project/docs/HARNESS.md") {
		t.Fatalf("incomplete file-edit approval = %#v, %v", fileRequest, ok)
	}

	commandBody, err := os.ReadFile("testdata/codex-command-approval-collapsed.txt")
	if err != nil {
		t.Fatal(err)
	}
	commandTail := string(commandBody[strings.Index(string(commandBody), "› 1."):])
	commandRequest, ok := ParseCodexCommandApproval(commandTail)
	if !ok || !commandRequest.Incomplete || commandRequest.OptionCount != 3 {
		t.Fatalf("incomplete command approval = %#v, %v", commandRequest, ok)
	}
	if fileRequest.DecisionID != requestDecisionID(t, string(fileBody)) {
		t.Fatal("full and reflowed file-edit approval do not share the one-shot identity")
	}
	other := strings.Replace(string(fileBody), "/work/project/docs/SESSION_SWITCH_LATENCY_TODO.md", "/work/project/docs/OTHER.md", 1)
	if requestDecisionID(t, other) == fileRequest.DecisionID {
		t.Fatal("distinct file-edit destinations share the one-shot identity")
	}
}

func requestDecisionID(t *testing.T, screen string) string {
	t.Helper()
	request, ok := ParseCodexCommandApproval(screen)
	if !ok {
		t.Fatal("approval fixture was not parsed")
	}
	return request.DecisionID
}

func TestCodexApprovalRejectsHeadinglessLookalikeFilePicker(t *testing.T) {
	lookalike := `model output copied from documentation
› 1. Yes, proceed (y)
2. Yes, and don't ask again for these files (a)
3. No, and tell Codex what to do differently (esc)
Press enter to confirm or esc to cancel`
	if request, ok := ParseCodexCommandApproval(lookalike); ok {
		t.Fatalf("headingless lookalike parsed as approval: %#v", request)
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
	screen  string
	keys    []string
	receipt string
}

func (f *approvalTerminal) Capture(context.Context) (string, error) { return f.screen, nil }
func (f *approvalTerminal) Input(context.Context, string) error     { panic("no prompt injection") }
func (f *approvalTerminal) Key(_ context.Context, key string) error {
	f.keys = append(f.keys, key)
	receipt := f.receipt
	if receipt == "" {
		receipt = "✔ You approved codex to run the command this time"
	}
	f.screen = receipt + "\n• Working (1s • esc to interrupt)\n› Ask Codex to do anything"
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

type chainedApprovalTerminal struct {
	approvalTerminal
	next string
}

func (f *chainedApprovalTerminal) Key(_ context.Context, key string) error {
	f.keys = append(f.keys, key)
	f.screen = "✔ You approved codex to run the first command this time\n" + f.next
	return nil
}

func TestCodexApprovalAcceptsReceiptFollowedImmediatelyByDifferentApproval(t *testing.T) {
	first := approvalFixture(t)
	secondBody, err := os.ReadFile("testdata/codex-command-approval-wiki.txt")
	if err != nil {
		t.Fatal(err)
	}
	request, _ := ParseCodexCommandApproval(first)
	terminal := &chainedApprovalTerminal{approvalTerminal: approvalTerminal{screen: first}, next: string(secondBody)}
	if err := AcceptCodexCommandOnce(context.Background(), terminal, request.Fingerprint); err != nil {
		t.Fatalf("direct approval-to-approval receipt was not confirmed: %v", err)
	}
	if strings.Join(terminal.keys, ",") != "Enter" {
		t.Fatalf("approval keys=%v, want one Enter", terminal.keys)
	}
}

func TestCodexApprovalRereadsBeforeInput(t *testing.T) {
	fixture := approvalFixture(t)
	request, _ := ParseCodexCommandApproval(fixture)
	terminal := &changingApprovalTerminal{approvalTerminal: approvalTerminal{screen: fixture}}
	if err := AcceptCodexCommandOnce(context.Background(), terminal, request.Fingerprint); !errors.Is(err, ErrStale) || len(terminal.keys) != 0 {
		t.Fatalf("disappeared request result=%v keys=%v, want typed stale without Enter", err, terminal.keys)
	}
}
