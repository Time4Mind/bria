package telegramcontroller_test

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
	"bria/internal/settingsport"
	"bria/internal/telegramcontroller"
)

type approvalLivenessNative struct {
	mu       sync.Mutex
	snapshot sessionruntime.NativeSnapshot
	updates  chan domain.SessionID
	controls chan sessionruntime.NativeRequest
}

func (native *approvalLivenessNative) NativeScreen(domain.SessionID) (sessionruntime.NativeSnapshot, bool) {
	native.mu.Lock()
	defer native.mu.Unlock()
	return native.snapshot, native.snapshot.Hash != ""
}

func (native *approvalLivenessNative) NativeScreenUpdates() <-chan domain.SessionID {
	return native.updates
}

func (native *approvalLivenessNative) NativeControl(_ context.Context, _ domain.SessionID, request sessionruntime.NativeRequest) (sessionruntime.NativeSnapshot, error) {
	native.controls <- request
	return sessionruntime.NativeSnapshot{
		Text: "approved", Hash: strings.Repeat("c", 64),
		ProviderSessionID: "provider", Generation: 1,
	}, nil
}

func (native *approvalLivenessNative) publish(id domain.SessionID, snapshot sessionruntime.NativeSnapshot) {
	native.mu.Lock()
	native.snapshot = snapshot
	native.mu.Unlock()
	native.updates <- id
}

// Native UI delivery and automatic command decisions are independent product
// boundaries. A stuck card publication must not stop a later approval scan.
func TestNativeApprovalContinuesWhileScreenNotificationIsBlocked(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "nativeapproval", "testdata", "codex-file-edits-approval.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider", 1)
	native := &approvalLivenessNative{
		updates:  make(chan domain.SessionID, 8),
		controls: make(chan sessionruntime.NativeRequest, 1),
	}
	custodyStarted := make(chan struct{}, 1)
	releaseCustody := make(chan struct{})
	preferences := &testPreferences{settings: settingsport.Snapshot{
		CardDetail: "standard", CardPageLimit: 64, AutoApproveCommands: true,
	}}
	controller := newController(t, nil, newLockedSessions(ready), nil, nil, telegramcontroller.Options{
		Recovered: []domain.Session{ready}, Native: native, Settings: preferences,
		DurableOutput: durableOutputFunc(func(_ context.Context, output telegramcontroller.OutgoingNotification) (telegramcontroller.OutputReceipt, error) {
			select {
			case custodyStarted <- struct{}{}:
			default:
			}
			<-releaseCustody
			return telegramcontroller.OutputReceipt{Inserted: true, SessionID: output.SessionID, OperationID: output.OperationID, Sequence: 1}, nil
		}),
	})
	defer func() {
		close(releaseCustody)
		if err := controller.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}()
	controller.StartNativeObserver()
	if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: ready.ID()}); err != nil {
		t.Fatal(err)
	}

	native.publish(ready.ID(), sessionruntime.NativeSnapshot{
		Text: "generic interactive screen", Hash: strings.Repeat("a", 64), Interactive: true,
		ProviderSessionID: "provider", Generation: 1,
	})
	select {
	case <-custodyStarted:
	case <-time.After(time.Second):
		t.Fatal("native screen projection did not reach durable custody")
	}
	native.publish(ready.ID(), sessionruntime.NativeSnapshot{
		Text: string(body), FullText: string(body), Hash: strings.Repeat("b", 64), Interactive: true,
		ProviderSessionID: "provider", Generation: 1,
	})
	select {
	case request := <-native.controls:
		if request.Key != "approve_once" || request.ExpectedHash != strings.Repeat("b", 64) {
			t.Fatalf("approval request = %+v", request)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("blocked native screen delivery stopped automatic approval")
	}
}
