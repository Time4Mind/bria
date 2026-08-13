package nodecontrol

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type sessionFileState struct{ state *domain.State }

func (s sessionFileState) State() *domain.State { return s.state }

func TestLocalSessionFileIsConfinedToWorkdir(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "report.txt")
	if err := os.WriteFile(inside, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := state.AddSession(domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Workdir: root,
		State: domain.SessionActive, RuntimeGeneration: 1, CreatedAt: now, LiveSinceAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewLocalSessionFileService("node", sessionFileState{state})
	if err != nil {
		t.Fatal(err)
	}
	query := SessionFileQuery{
		ActorID: 7, NodeID: "node", SessionID: "session", ExpectedGeneration: 1,
		Path: "report.txt",
	}
	file, err := service.OpenSessionFile(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Content.Close()
	content, err := io.ReadAll(file.Content)
	if err != nil || string(content) != "report" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	query.Path = outside
	if _, err := service.OpenSessionFile(context.Background(), query); err == nil {
		t.Fatal("file outside workdir was exposed")
	}
}
