package application_test

import (
	"context"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func createService(t *testing.T, backend string) (*application.Service, *clusterstate.Machine) {
	t.Helper()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "alpha", Name: "Alpha", Status: domain.NodeOnline,
		Backends: []domain.BackendDescriptor{{Name: backend, Capabilities: []string{"session.create", "session.resume"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "alpha"); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	port := localMachine{machine: machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	return service, machine
}

func TestCreateSessionCommitsStartingIntentAndSelection(t *testing.T) {
	service, machine := createService(t, "claude")
	ctx := application.WithOperationScope(context.Background(), "telegram-42")
	request := application.CreateSessionRequest{NodeID: "alpha", Backend: "claude", Workdir: t.TempDir()}
	created, err := service.CreateSession(ctx, application.Principal{UserID: 7}, request)
	if err != nil {
		t.Fatal(err)
	}
	if created.RuntimePhase != domain.RuntimeStarting || created.ProviderSessionID == "" || created.ProviderResume {
		t.Fatalf("created=%#v", created)
	}
	if !created.UserRequestTracked || created.UserRequestSeen {
		t.Fatalf("fresh request tracking=%#v", created)
	}
	state := machine.State()
	if got := state.Navigation.ActiveSessionByUserNode[7]["alpha"]; got != created.ID {
		t.Fatalf("active session=%q", got)
	}
	again, err := service.CreateSession(ctx, application.Principal{UserID: 7}, request)
	if err != nil || again.ID != created.ID || len(machine.State().Sessions) != 1 {
		t.Fatalf("replay=%#v err=%v sessions=%d", again, err, len(machine.State().Sessions))
	}
}

func TestCreateCodexResumeAndProviderBinding(t *testing.T) {
	service, machine := createService(t, "codex")
	actor := application.Principal{UserID: 7}
	created, err := service.CreateSession(context.Background(), actor, application.CreateSessionRequest{
		NodeID: "alpha", Backend: "codex", Workdir: t.TempDir(), ProviderSessionID: "resume-id",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created.ProviderResume || created.ProviderSessionID != "resume-id" {
		t.Fatalf("resume metadata=%#v", created)
	}
	if created.UserRequestTracked || created.UserRequestSeen {
		t.Fatalf("resumed request tracking=%#v", created)
	}
	fresh, err := service.CreateSession(context.Background(), actor, application.CreateSessionRequest{
		NodeID: "alpha", Backend: "codex", Workdir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.BindProviderSession(context.Background(), actor, fresh, "discovered-id"); err != nil {
		t.Fatal(err)
	}
	if got := machine.State().Sessions[fresh.Ref().Key()].ProviderSessionID; got != "discovered-id" {
		t.Fatalf("provider id=%q", got)
	}
}

func TestCreateSessionFailsClosedWhenRequiredIsolationIsNotReady(t *testing.T) {
	service, machine := createService(t, "codex")
	state := machine.State()
	if err := state.SetNodeBackendIsolationRequired("alpha", true); err != nil {
		t.Fatal(err)
	}
	machine = clusterstate.NewMachine(state)
	port := localMachine{machine: machine}
	service, _ = application.NewService(port, port)
	_, err := service.CreateSession(context.Background(), application.Principal{UserID: 7}, application.CreateSessionRequest{
		NodeID: "alpha", Backend: "codex", Workdir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("session started without required backend isolation")
	}
}
