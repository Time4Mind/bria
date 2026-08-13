package domain_test

import (
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestProviderAccountAliasRequiresKnownBackendAndClones(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "node", Name: "Node",
		Backends: []domain.BackendDescriptor{{Name: "Codex"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetProviderAccountAlias("node", "codex", "Personal"); err != nil {
		t.Fatal(err)
	}
	if got := state.ProviderAccountAlias("node", "CODEX"); got != "Personal" {
		t.Fatalf("alias=%q", got)
	}
	clone := state.Clone()
	clone.ProviderAccountAliases[domain.ProviderAccountAliasKey("node", "codex")] = "Changed"
	if got := state.ProviderAccountAlias("node", "codex"); got != "Personal" {
		t.Fatalf("clone mutated source alias=%q", got)
	}
	if err := state.SetProviderAccountAlias("node", "claude", "Other"); err == nil {
		t.Fatal("alias accepted for an unavailable backend")
	}
	if err := state.SetProviderAccountAlias("node", "codex", ""); err != nil {
		t.Fatal(err)
	}
	if got := state.ProviderAccountAlias("node", "codex"); got != "" {
		t.Fatalf("cleared alias=%q", got)
	}
}
