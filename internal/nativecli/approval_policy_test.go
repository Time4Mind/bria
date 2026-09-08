package nativecli

import (
	"strings"
	"testing"

	"bria/internal/domain"
)

func TestCodexInlineConfigCannotConflictWithOwnedApprovalPolicy(t *testing.T) {
	for _, args := range [][]string{
		{"codex", "-c", "approval_policy=\"never\""},
		{"codex", "--config=approval_policy=\"never\""},
		{"codex", "-c", " sandbox_mode = \"danger-full-access\""},
		{"codex", "--config", "\"approval_policy\" = \"never\""},
	} {
		p, err := Build(domain.ProviderCodex, args, "/work", "")
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(p.Command, " ")
		if strings.Contains(joined, "never") || strings.Contains(joined, "danger-full-access") || !strings.Contains(joined, "--ask-for-approval on-request --sandbox workspace-write") {
			t.Fatalf("conflicting launch policy: %v", p.Command)
		}
	}
}

func TestExactCCBotHookBypassFlagsAreNotASupportedBriaMode(t *testing.T) {
	_, err := Build(domain.ProviderCodex, []string{"codex", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", "--enable", "hooks", "--no-alt-screen"}, "/work", "")
	if err == nil {
		t.Fatal("undocumented CCBot mode silently enabled")
	}
}
