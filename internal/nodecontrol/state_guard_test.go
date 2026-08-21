package nodecontrol

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func TestStateGuardReauthorizesSharedControl(t *testing.T) {
	state := controlState(t)
	ref := domain.SessionRef{NodeID: "target", SessionID: "s"}
	if err := state.ShareSession(1, ref, 2, domain.ShareControl); err != nil {
		t.Fatal(err)
	}
	guard, err := NewStateGuard(staticState{state})
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeRequest(runtimehost.ActionStop)
	request.ActorID = 2
	for _, action := range []runtimehost.Action{runtimehost.ActionStop, runtimehost.ActionSendKey} {
		request.Action = action
		if err := guard.AuthorizeRuntime(context.Background(), request); err != nil {
			t.Fatalf("shared %s rejected: %v", action, err)
		}
	}
	request.Action = runtimehost.ActionClose
	request.ArchiveCommitID = "archive"
	if err := guard.AuthorizeRuntime(context.Background(), request); err == nil {
		t.Fatal("shared close accepted")
	}
}

func TestStateGuardAllowsOnlyOwnerToDiscardMarkedSession(t *testing.T) {
	state := controlState(t)
	ref := domain.SessionRef{NodeID: "target", SessionID: "s"}
	session := state.Sessions[ref.Key()]
	if err := state.DiscardSession(1, ref, session.Revision, time.Now()); err != nil {
		t.Fatal(err)
	}
	guard, err := NewStateGuard(staticState{state})
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeRequest(runtimehost.ActionDiscard)
	if err := guard.AuthorizeRuntime(context.Background(), request); err != nil {
		t.Fatalf("owner discard rejected: %v", err)
	}
	request.ActorID = 2
	if err := guard.AuthorizeRuntime(context.Background(), request); err == nil {
		t.Fatal("shared user discarded session")
	}
}
