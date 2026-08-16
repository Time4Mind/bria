package telegramui

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestEnrollmentMethodsPreferInvitation(t *testing.T) {
	screen := RenderEnrollmentMethods(englishCopy)
	assertGoldenGrid(t, screen, `[Invitation -> cluster_invite]
[Node contract -> cluster_contract]
[← Back -> settings_cat@cluster]`)
}

func TestNodeMembershipRequiresDisableBeforeDelete(t *testing.T) {
	active := RenderNodeMembership(NodeMembershipInput{
		Copy: englishCopy, Node: domain.Node{ID: "a", Name: "Alpha", Lifecycle: domain.NodeActive},
		Backends: "codex", CanDisable: true, DisableToken: "disable", RenameToken: "rename",
	})
	grid := CanonicalGrid(active.Grid)
	if strings.Contains(grid, "Delete") || !strings.Contains(grid, "Disable") {
		t.Fatalf("active node actions:\n%s", grid)
	}
	disabled := RenderNodeMembership(NodeMembershipInput{
		Copy: englishCopy, Node: domain.Node{ID: "a", Name: "Alpha", Lifecycle: domain.NodeDisabled},
		Backends: "codex", EnableToken: "enable", DeleteToken: "delete", RenameToken: "rename",
	})
	grid = CanonicalGrid(disabled.Grid)
	if !strings.Contains(grid, "Enable") || !strings.Contains(grid, "Delete") ||
		strings.Contains(grid, "Disable") {
		t.Fatalf("disabled node actions:\n%s", grid)
	}
}

func TestFinalAvailableNodeHasNoDisableAction(t *testing.T) {
	screen := RenderNodeMembership(NodeMembershipInput{
		Copy: englishCopy, Node: domain.Node{ID: "a", Name: "Alpha", Lifecycle: domain.NodeActive},
		Backends: "codex", RenameToken: "rename",
	})
	if grid := CanonicalGrid(screen.Grid); strings.Contains(grid, "Disable") {
		t.Fatalf("final node actions:\n%s", grid)
	}
}

func TestNodeMembershipCollapsesBackendManagement(t *testing.T) {
	screen := RenderNodeMembership(NodeMembershipInput{
		Copy: englishCopy, Node: domain.Node{ID: "a", Name: "Alpha"}, Backends: "codex",
		RenameToken: "rename", BackendsToken: "backends",
	})
	if grid := CanonicalGrid(screen.Grid); grid != `[Rename -> node_rename@rename]
[Backends -> node_backends@backends]
[← Back -> status_mode@settings]` {
		t.Fatalf("backend group missing:\n%s", grid)
	}
}

func TestBackendMenuDistinguishesInstallConnectAndDisconnect(t *testing.T) {
	list := RenderNodeBackends(NodeBackendsInput{
		Copy: englishCopy, NodeName: "Alpha", BackToken: "node",
		Items: []NodeBackendItem{
			{Name: "claude", OpenToken: "claude"},
			{Name: "codex", Installed: true, OpenToken: "codex"},
			{Name: "other", Installed: true, Connected: true, OpenToken: "other"},
		},
	})
	assertGoldenGrid(t, list, `[claude · not installed -> node_backend@claude]
[codex · installed -> node_backend@codex]
[other · connected -> node_backend@other]
[← Back -> node_settings@node]`)

	for _, test := range []struct {
		item     NodeBackendItem
		expected string
	}{
		{NodeBackendItem{Name: "claude", Token: "install"}, "[install -> backend_install@install]"},
		{NodeBackendItem{Name: "codex", Installed: true, Token: "connect"}, "[connect -> backend_connect@connect]"},
		{NodeBackendItem{Name: "other", Installed: true, Connected: true, Token: "disconnect"}, "[disconnect -> backend_remove@disconnect]"},
	} {
		detail := RenderNodeBackendDetail(NodeBackendDetailInput{
			Copy: englishCopy, NodeName: "Alpha", Backend: test.item, BackToken: "backends",
		})
		if grid := CanonicalGrid(detail.Grid); !strings.Contains(grid, test.expected) {
			t.Fatalf("missing %s in:\n%s", test.expected, grid)
		}
	}
}

func TestNodeMembershipCanReturnToCallingNode(t *testing.T) {
	screen := RenderNodeMembership(NodeMembershipInput{
		Copy: englishCopy, Node: domain.Node{ID: "a", Name: "Alpha"}, Backends: "—",
		RenameToken: "rename", BackAction: ActionSelectNode, BackToken: "node-alpha",
	})
	if grid := CanonicalGrid(screen.Grid); !strings.Contains(grid, "[← Back -> node@node-alpha]") {
		t.Fatalf("caller-aware back action missing:\n%s", grid)
	}
}

func TestProviderAuthenticationChallengesUseBoundedButtons(t *testing.T) {
	claude := RenderProviderAuthChallenge(
		englishCopy, "claude", "https://claude.com/auth", "", true, "cancel",
	)
	if grid := CanonicalGrid(claude.Grid); grid != "[Cancel -> provider_auth_cancel@cancel]" {
		t.Fatalf("Claude challenge grid:\n%s", grid)
	}
	if err := claude.Validate(); err != nil {
		t.Fatal(err)
	}
	codex := RenderProviderAuthChallenge(
		englishCopy, "codex", "https://auth.openai.com/device", "ABCD-EFGH", false, "cancel",
	)
	if !strings.Contains(codex.Text, "ABCD-EFGH") {
		t.Fatalf("Codex device code missing: %q", codex.Text)
	}
	if err := codex.Validate(); err != nil {
		t.Fatal(err)
	}
}
