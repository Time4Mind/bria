package settingscomposition_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/telegramcontroller"
)

type approvalNative struct {
	mu       sync.Mutex
	snapshot sessionruntime.NativeSnapshot
	updates  chan domain.SessionID
	requests chan sessionruntime.NativeRequest
}

func (n *approvalNative) NativeScreen(domain.SessionID) (sessionruntime.NativeSnapshot, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.snapshot, true
}
func (n *approvalNative) NativeScreenUpdates() <-chan domain.SessionID { return n.updates }
func (n *approvalNative) NativeControl(_ context.Context, _ domain.SessionID, r sessionruntime.NativeRequest) (sessionruntime.NativeSnapshot, error) {
	n.requests <- r
	n.mu.Lock()
	defer n.mu.Unlock()
	n.snapshot = sessionruntime.NativeSnapshot{Text: "approved", Hash: strings.Repeat("b", 64), ProviderSessionID: "provider", Generation: 1}
	return n.snapshot, nil
}

func TestAutoApprovalOwnerSettingGatesPendingFullAndCollapsedDialogs(t *testing.T) {
	for _, name := range []string{"codex-command-approval.txt", "codex-command-approval-collapsed.txt"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			body, err := os.ReadFile(filepath.Join("..", "nativeapproval", "testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "settings.json")
			store, err := settings.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			prefs := settingscomposition.Preferences{Store: store}
			starting, err := domain.NewStartingSession("11111111-1111-4111-9111-111111111111", "approval-test", "local", domain.ProviderCodex, "/work")
			if err != nil {
				t.Fatal(err)
			}
			ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider", Generation: 1})
			if err != nil {
				t.Fatal(err)
			}
			n := &approvalNative{snapshot: sessionruntime.NativeSnapshot{Text: string(body), FullText: string(body), Hash: strings.Repeat("a", 64), Interactive: true, ProviderSessionID: "provider", Generation: 1}, updates: make(chan domain.SessionID, 8), requests: make(chan sessionruntime.NativeRequest, 8)}
			c, err := telegramcontroller.New(42, 42, "local", settingsControllerCreator{}, approvalSessions{ready}, settingsControllerSubmit{}, settingsControllerNotifier{}, telegramcontroller.Options{Recovered: []domain.Session{ready}, Settings: prefs, Native: n})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close(ctx) })
			// The dialog already exists when the owner disables automatic input.
			toggle := func() {
				t.Helper()
				r, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsAutoApproveCommands})
				if err != nil || r.Surface == nil || !strings.Contains(r.Surface.Text, "Автоподтверждение Codex") {
					t.Fatalf("settings surface=%+v err=%v", r, err)
				}
			}
			toggle()
			reopened, err := settings.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			disk, err := (settingscomposition.Preferences{Store: reopened}).Snapshot(ctx)
			if err != nil || disk.AutoApproveCommands {
				t.Fatal("OFF was not durably persisted", err)
			}
			c.StartNativeObserver()
			n.updates <- ready.ID()
			select {
			case r := <-n.requests:
				t.Fatalf("OFF sent %+v", r)
			case <-time.After(150 * time.Millisecond):
			}
			// A visible settings surface must not prevent enabled background approvals.
			toggle()
			n.updates <- ready.ID()
			select {
			case r := <-n.requests:
				if r.Key != "approve_once" || r.ExpectedHash != strings.Repeat("a", 64) || r.ExpectedProviderSessionID != "provider" || r.ExpectedGeneration != 1 {
					t.Fatalf("unfenced request %+v", r)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("ON did not release pending approval")
			}
			toggle()
			n.mu.Lock()
			n.snapshot = sessionruntime.NativeSnapshot{Text: string(body), FullText: string(body), Hash: strings.Repeat("c", 64), Interactive: true, ProviderSessionID: "provider", Generation: 2}
			n.mu.Unlock()
			n.updates <- ready.ID()
			select {
			case r := <-n.requests:
				t.Fatalf("OFF sent later approval %+v", r)
			case <-time.After(150 * time.Millisecond):
			}
		})
	}
}

type approvalSessions struct{ ready domain.Session }

func (s approvalSessions) List(context.Context) ([]domain.Session, error) {
	return []domain.Session{s.ready}, nil
}
func (s approvalSessions) Load(context.Context, domain.SessionID) (domain.Session, error) {
	return s.ready, nil
}
