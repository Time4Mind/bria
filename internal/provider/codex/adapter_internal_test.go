package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"bria/internal/runtimeprotocol"
)

func TestProviderInteractionsPreserveCommandAndFileApprovalContract(t *testing.T) {
	tests := []struct {
		name    string
		request ServerRequest
		want    runtimeprotocol.InteractionRequest
	}{
		{
			name:    "command",
			request: ServerRequest{InteractionID: "number:1", Kind: ServerRequestCommandPermission, ThreadID: "thread", TurnID: "turn", Permission: &PermissionRequest{ItemID: "cmd", ApprovalID: "approval", StartedAtMS: 9, Reason: "needed", Command: "go test ./...", Cwd: "/work"}},
			want:    runtimeprotocol.InteractionRequest{ID: "number:1", Kind: runtimeprotocol.InteractionCommandApproval, ThreadID: "thread", TurnID: "turn", ItemID: "cmd", ApprovalID: "approval", StartedAtMS: 9, Reason: "needed", Command: "go test ./...", Cwd: "/work", Decisions: allApprovalDecisions()},
		},
		{
			name:    "file",
			request: ServerRequest{InteractionID: "string:file", Kind: ServerRequestFilePermission, ThreadID: "thread", TurnID: "turn", Permission: &PermissionRequest{ItemID: "patch", StartedAtMS: 11, Reason: "edit", GrantRoot: "/work"}},
			want:    runtimeprotocol.InteractionRequest{ID: "string:file", Kind: runtimeprotocol.InteractionFileApproval, ThreadID: "thread", TurnID: "turn", ItemID: "patch", StartedAtMS: 11, Reason: "edit", GrantRoot: "/work", Decisions: allApprovalDecisions()},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := providerInteraction(test.request)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("interaction = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestResolvedRawExecutableIsCanonicalAndUnaffectedBySymlinkRetarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink retarget fixture is Unix-specific")
	}
	original, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatalf("EvalSymlinks(test binary): %v", err)
	}
	link := filepath.Join(t.TempDir(), "codex")
	if err := os.Symlink(original, link); err != nil {
		t.Fatalf("Symlink(test binary): %v", err)
	}
	resolved, err := resolveRawExecutable(link)
	if err != nil {
		t.Fatalf("resolveRawExecutable() error = %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatalf("Remove(symlink): %v", err)
	}
	if err := os.Symlink("/bin/sh", link); err != nil {
		t.Fatalf("retarget symlink: %v", err)
	}
	if resolved != original {
		t.Fatalf("resolveRawExecutable() = %q, want canonical immutable target %q", resolved, original)
	}
}

func TestDecodedInterruptIsLatchedBeforeBlockedMainLoopEnqueue(t *testing.T) {
	session := &adapterSession{active: &activeAdapterTurn{requestID: "request-1", turnID: "turn-1"}}
	requests := make(chan adapterRequest)
	done := make(chan error, 1)
	stop := make(chan struct{})
	go readAdapterRequests(
		strings.NewReader(`{"protocol":1,"type":"interrupt","request_id":"request-1"}`+"\n"),
		DefaultMaxParentMessageBytes, requests, done, stop, session.latchDecodedRequest,
	)
	deadline := time.Now().Add(time.Second)
	for {
		session.mu.Lock()
		latched := session.active.interruptRequested
		session.mu.Unlock()
		if latched {
			break
		}
		if time.Now().After(deadline) {
			close(stop)
			t.Fatal("decoded interrupt was not latched while enqueue remained blocked")
		}
		time.Sleep(time.Millisecond)
	}
	if !session.claimPendingInterrupt("request-1") {
		close(stop)
		t.Fatal("accepted turn could not claim the already-decoded interrupt")
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("readAdapterRequests() error = %v", err)
	}
}

func TestInterruptDecodedAfterAcceptedHookWinsBeforeTerminalPublication(t *testing.T) {
	session := &adapterSession{active: &activeAdapterTurn{
		requestID: "request-1", turnID: "turn-1", accepted: true,
	}}
	if session.claimPendingInterrupt("request-1") {
		t.Fatal("accepted hook claimed an interrupt before ingress")
	}

	// This is the exact semantic window from the regression: the hook has
	// returned, StartTurn has a successful terminal, but no final is committed
	// to the parent wire yet.
	session.latchDecodedRequest(adapterRequest{Type: "interrupt", RequestID: "request-1"})
	closing, accepted, cancellationWon := session.commitTerminalPublication("request-1")
	if closing || !accepted || !cancellationWon {
		t.Fatalf("terminal arbitration = closing:%t accepted:%t cancellation:%t", closing, accepted, cancellationWon)
	}

	// Once terminal publication owns the linearization point, later ingress
	// cannot retroactively turn the already-committed result into cancellation.
	committed := &adapterSession{active: &activeAdapterTurn{
		requestID: "request-2", turnID: "turn-2", accepted: true,
	}}
	_, _, _ = committed.commitTerminalPublication("request-2")
	committed.latchDecodedRequest(adapterRequest{Type: "interrupt", RequestID: "request-2"})
	if committed.active.interruptRequested {
		t.Fatal("post-commit interrupt changed terminal ownership")
	}
}
