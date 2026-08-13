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

func TestNodeMembershipListsProviderAliases(t *testing.T) {
	screen := RenderNodeMembership(NodeMembershipInput{
		Copy: englishCopy, Node: domain.Node{ID: "a", Name: "Alpha"}, Backends: "codex",
		RenameToken: "rename", ProviderAliases: []ProviderAliasItem{
			{Backend: "codex", Alias: "Personal", Token: "provider", AuthToken: "auth"},
		},
	})
	if grid := CanonicalGrid(screen.Grid); grid != `[Rename -> node_rename@rename]
[Account · codex: Personal -> provider_alias@provider]
[🔐 Sign in · codex -> provider_auth@auth]
[← Back -> status_mode@settings]` {
		t.Fatalf("provider alias missing:\n%s", grid)
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
