package storage_test

import (
	"context"
	"path/filepath"
	"testing"

	"bria/internal/domain"
	"bria/internal/storage"
)

func TestBackgroundNodeFallbackDoesNotNavigateForeground(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeActiveSession(ctx, "foreground", "11111111-1111-4111-9111-111111111111"); err != nil {
		t.Fatal(err)
	}
	port, ok := any(s).(interface {
		SetNodeLastActiveSession(context.Context, domain.ComputerID, domain.SessionID) error
	})
	if !ok {
		t.Fatal("background fallback requires foreground-changing writes")
	}
	if err := port.SetNodeLastActiveSession(ctx, "background", "22222222-2222-4222-9222-222222222222"); err != nil {
		t.Fatal(err)
	}
	reread, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	node, active, err := reread.LoadNodeSelection(ctx)
	global, globalErr := reread.LoadActiveSession(ctx)
	if err != nil || globalErr != nil || node != "foreground" || global != active["foreground"] || active["background"] != "22222222-2222-4222-9222-222222222222" {
		t.Fatalf("node=%q active=%v global=%q errors=%v/%v", node, active, global, err, globalErr)
	}
}
