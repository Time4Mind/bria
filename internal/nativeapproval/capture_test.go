package nativeapproval

import (
	"context"
	"os"
	"strings"
	"testing"
)

type expandingTerminal struct {
	approvalTerminal
	full       string
	expansions int
}

func (t *expandingTerminal) Expand(_ context.Context, rows int) error {
	t.expansions++
	if rows >= 80 {
		t.screen = t.full
	}
	return nil
}

func TestOversizedApprovalCanBeExpandedForCompletePreview(t *testing.T) {
	b, err := os.ReadFile("testdata/codex-command-approval-collapsed.txt")
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := ParseCodexCommandApproval(string(b)); !ok || !r.Incomplete {
		t.Fatal("hidden command dialog not marked incomplete")
	}
	terminal := &expandingTerminal{approvalTerminal: approvalTerminal{screen: string(b)}, full: approvalFixture(t)}
	full, err := CaptureComplete(context.Background(), terminal)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ParseCodexCommandApproval(full); !ok || terminal.expansions != 1 || len(terminal.keys) != 0 {
		t.Fatal("oversized dialog not recovered without key", terminal.expansions)
	}
}

func TestOrdinaryScreenDoesNotResize(t *testing.T) {
	terminal := &expandingTerminal{approvalTerminal: approvalTerminal{screen: "ordinary answer"}}
	text, err := CaptureComplete(context.Background(), terminal)
	if err != nil || !strings.Contains(text, "ordinary") || terminal.expansions != 0 {
		t.Fatal("ordinary output resized")
	}
}

func TestBeyondMaximumCanvasRetainsRecognizedIncompleteApproval(t *testing.T) {
	b, err := os.ReadFile("testdata/codex-command-approval-collapsed.txt")
	if err != nil {
		t.Fatal(err)
	}
	terminal := &expandingTerminal{approvalTerminal: approvalTerminal{screen: string(b)}, full: string(b)}
	screen, err := CaptureComplete(context.Background(), terminal)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.expansions != 3 || len(terminal.keys) != 0 || !NeedsExpansion(screen) {
		t.Fatal("unbounded or side-effecting overflow recovery")
	}
	if r, ok := ParseCodexCommandApproval(screen); !ok || !r.Incomplete {
		t.Fatal("over-budget request lost recognized incomplete state")
	}
}
