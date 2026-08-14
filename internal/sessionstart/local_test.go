package sessionstart

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerbinding"
	"github.com/Time4Mind/bria/internal/transcript"
)

func TestLocalDiscoverForBindingRejectsNewerForeignCodexSession(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "mac", Name: "Mac", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "mac"); err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(t.TempDir(), "workdir")
	if err := os.Mkdir(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	codexRoot := filepath.Join(t.TempDir(), "codex")
	directory := filepath.Join(codexRoot, "2026", "08", "14")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	writeRollout := func(name, id string) {
		t.Helper()
		content := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}`+"\n", id, workdir)
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeRollout("rollout-own.jsonl", "own-provider")
	writeRollout("rollout-foreign.jsonl", "ccbot-provider")
	reader, err := transcript.NewReader(transcript.Config{
		ClaudeProjectsRoot: t.TempDir(), CodexSessionsRoot: codexRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := providerbinding.NewStore(filepath.Join(t.TempDir(), "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bindings.Put(providerbinding.Record{
		NodeID: "mac", SessionID: "bria-session", ProviderSessionID: "own-provider",
		Workdir: workdir, TmuxSession: "bria", TmuxWindow: "bria-window", UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	local := &Local{
		nodeID: "mac", state: controllerPort{machine: clusterstate.NewMachine(state)},
		transcripts: reader, bindings: bindings,
	}
	discovery, err := local.Discover(context.Background(), DiscoverRequest{
		ActorID: 7, NodeID: "mac", Session: domain.SessionRef{NodeID: "mac", SessionID: "bria-session"},
		Backend: "codex", Workdir: workdir, Limit: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Total != 1 || len(discovery.Candidates) != 1 ||
		discovery.Candidates[0].ProviderSessionID != "own-provider" {
		t.Fatalf("binding discovery leaked foreign candidate: %#v", discovery)
	}
}
