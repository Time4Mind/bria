package nativecli

import (
	"bria/internal/domain"
	"context"
	"strings"
	"testing"
)

func TestRootClaudeUsesAllowedAutoModeWithoutFakingSandbox(t *testing.T) {
	for _, test := range []struct {
		policy   LaunchPolicy
		wantAuto bool
	}{
		{LaunchPolicy{Root: true}, true}, {LaunchPolicy{Root: true, ExistingCLISandbox: true}, false}, {LaunchPolicy{}, false},
	} {
		plan, err := BuildWithPolicy(domain.ProviderClaude, []string{"/fixture/claude", "--dangerously-skip-permissions"}, "/work", testID, test.policy)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(plan.Command, " ")
		if strings.Contains(joined, "--permission-mode auto") != test.wantAuto || strings.Contains(joined, "--dangerously-skip-permissions") == test.wantAuto {
			t.Fatal("permission fallback mismatch")
		}
		if !strings.Contains(joined, "--resume "+testID) {
			t.Fatal("permission fallback lost resume identity")
		}
	}
	plan, err := BuildWithPolicy(domain.ProviderCodex, []string{"/fixture/codex"}, "/work", testID, LaunchPolicy{Root: true})
	if err != nil || !strings.Contains(strings.Join(plan.Command, " "), "--ask-for-approval on-request --sandbox workspace-write") {
		t.Fatal("Claude fallback altered Codex")
	}
}

func TestReadyClassifiesRootBypassRejectionWithoutInput(t *testing.T) {
	terminal := &fakeTerminal{screens: []string{"--dangerously-skip-permissions cannot be used with root/sudo privileges for security reasons"}}
	_, err := Ready(context.Background(), terminal, Plan{Provider: domain.ProviderClaude, SessionID: testID})
	if err == nil || !strings.Contains(err.Error(), "bypass forbidden") || len(terminal.inputs) != 0 || len(terminal.keys) != 0 {
		t.Fatal("root guard not classified safely")
	}
}
