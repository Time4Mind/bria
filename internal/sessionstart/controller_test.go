package sessionstart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

type controllerPort struct{ machine *clusterstate.Machine }

func (p controllerPort) State() *domain.State { return p.machine.State() }
func (p controllerPort) Apply(_ context.Context, command clusterstate.Command) (clusterstate.Result, error) {
	data, err := json.Marshal(command)
	if err != nil {
		return clusterstate.Result{}, err
	}
	var copied clusterstate.Command
	if err := json.Unmarshal(data, &copied); err != nil {
		return clusterstate.Result{}, err
	}
	return p.machine.Apply(copied), nil
}

type startServiceStub struct {
	provisionErr error
	provisions   int
	discoveries  []transcript.Candidate
	requests     []DiscoverRequest
}

func (*startServiceStub) Browse(context.Context, BrowseRequest) (BrowseResult, error) {
	return BrowseResult{}, nil
}
func (s *startServiceStub) Discover(_ context.Context, request DiscoverRequest) (transcript.Discovery, error) {
	s.requests = append(s.requests, request)
	items := append([]transcript.Candidate(nil), s.discoveries...)
	return transcript.Discovery{Candidates: items, Total: len(items)}, nil
}
func (s *startServiceStub) Provision(context.Context, ProvisionRequest) error {
	s.provisions++
	return s.provisionErr
}

type leaderStub bool

func (l leaderStub) IsLeader() bool { return bool(l) }

func newControllerFixture(t *testing.T) (*Controller, *clusterstate.Machine, *startServiceStub) {
	t.Helper()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "alpha", Name: "Alpha", Status: domain.NodeOnline,
		Backends: []domain.BackendDescriptor{{
			Name: "codex", Capabilities: []string{"session.create", "session.resume"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "alpha"); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	port := controllerPort{machine: machine}
	app, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	router := &startServiceStub{discoveries: []transcript.Candidate{{ProviderSessionID: "provider-new"}}}
	controller, err := NewController(app, port, router, leaderStub(true))
	if err != nil {
		t.Fatal(err)
	}
	return controller, machine, router
}

func TestCreateProvisionsAndPublishesIdle(t *testing.T) {
	controller, machine, router := newControllerFixture(t)
	session, err := controller.Create(context.Background(), application.Principal{UserID: 7}, application.CreateSessionRequest{
		NodeID: "alpha", Backend: "codex", Workdir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if router.provisions != 1 {
		t.Fatalf("provisions=%d", router.provisions)
	}
	if got := machine.State().Sessions[session.Ref().Key()].RuntimePhase; got != domain.RuntimeIdle {
		t.Fatalf("phase=%q", got)
	}
	if len(router.requests) != 1 || router.requests[0].Session != session.Ref() {
		t.Fatalf("provider discovery was not scoped to created session: %#v", router.requests)
	}
}

func TestReconcileStartsRunningWhenInputWasQueuedDuringProvision(t *testing.T) {
	controller, machine, _ := newControllerFixture(t)
	created := time.Now().UTC()
	session := domain.Session{
		ID: "queued", NodeID: "alpha", OwnerID: 7, Backend: "codex", Workdir: t.TempDir(),
		State: domain.SessionLive, RuntimePhase: domain.RuntimeStarting,
		LastOperation: &domain.SessionOperationResult{
			OperationID: "input", Action: domain.ActionSendInput,
			Status: domain.OperationQueued, At: created,
		},
		CreatedAt: created, LiveSinceAt: created, LastEventAt: created,
	}
	command, err := clusterstate.NewCommand("add-queued", clusterstate.CommandAddSession, created, session)
	if err != nil {
		t.Fatal(err)
	}
	if result := machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	controller.reconcile(context.Background())
	if got := machine.State().Sessions[session.Ref().Key()].RuntimePhase; got != domain.RuntimeRunning {
		t.Fatalf("phase=%q", got)
	}
}

func TestCodexResumeWaitsForExactProviderBinding(t *testing.T) {
	controller, machine, router := newControllerFixture(t)
	router.discoveries = nil
	session, err := controller.Create(
		context.Background(), application.Principal{UserID: 7},
		application.CreateSessionRequest{
			NodeID: "alpha", Backend: "codex", Workdir: t.TempDir(),
			ProviderSessionID: "provider-resume",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := machine.State().Sessions[session.Ref().Key()].RuntimePhase; got != domain.RuntimeStarting {
		t.Fatalf("unbound resume phase=%q", got)
	}
	if len(router.requests) != 1 || router.requests[0].Session != session.Ref() ||
		router.requests[0].After != session.ProviderBindingSince {
		t.Fatalf("resume binding discovery=%#v", router.requests)
	}

	router.discoveries = []transcript.Candidate{{ProviderSessionID: "foreign-provider"}}
	controller.reconcile(context.Background())
	if got := machine.State().Sessions[session.Ref().Key()].RuntimePhase; got != domain.RuntimeStarting {
		t.Fatalf("foreign binding promoted resume: phase=%q", got)
	}

	router.discoveries = []transcript.Candidate{{ProviderSessionID: "provider-resume"}}
	controller.reconcile(context.Background())
	if got := machine.State().Sessions[session.Ref().Key()].RuntimePhase; got != domain.RuntimeIdle {
		t.Fatalf("confirmed resume phase=%q", got)
	}
}

func TestReconcileArchivesPersistentlyFailedStart(t *testing.T) {
	controller, machine, router := newControllerFixture(t)
	router.provisionErr = errors.New("backend unavailable")
	created := time.Now().Add(-5 * time.Minute).UTC()
	session := domain.Session{
		ID: "failed", NodeID: "alpha", OwnerID: 7, Backend: "codex", Workdir: t.TempDir(),
		State: domain.SessionLive, RuntimePhase: domain.RuntimeStarting,
		CreatedAt: created, LiveSinceAt: created, LastEventAt: created,
	}
	command, err := clusterstate.NewCommand("add-failed", clusterstate.CommandAddSession, created, session)
	if err != nil {
		t.Fatal(err)
	}
	if result := machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	controller.reconcile(context.Background())
	got := machine.State().Sessions[session.Ref().Key()]
	if got.State != domain.SessionArchived || got.ArchiveReason != domain.ArchiveResumeFailed {
		t.Fatalf("failed start=%#v", got)
	}
}

func TestReconcileKeepsCodexResumeWhenProviderStartIsPending(t *testing.T) {
	controller, machine, router := newControllerFixture(t)
	created := time.Now().Add(-2*controller.startTimeout + time.Second).UTC()
	session := domain.Session{
		ID: "resume-pending", NodeID: "alpha", OwnerID: 7, Backend: "codex", Workdir: t.TempDir(),
		ProviderSessionID: "ccbot-provider", ProviderResume: true,
		State: domain.SessionLive, RuntimePhase: domain.RuntimeStarting,
		CreatedAt: created, LiveSinceAt: created, LastEventAt: created,
	}
	command, err := clusterstate.NewCommand("add-resume-pending", clusterstate.CommandAddSession, created, session)
	if err != nil {
		t.Fatal(err)
	}
	if result := machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	// The local start adapter wraps a Codex resume startup failure with the
	// retryable sentinel. Reproduce that boundary here without invoking tmux.
	router.provisionErr = fmt.Errorf("%w: provider exited during startup", errProviderResumePending)
	controller.reconcile(context.Background())
	controller.reconcile(context.Background())
	got := machine.State().Sessions[session.Ref().Key()]
	if !got.IsLive() || got.RuntimePhase != domain.RuntimeStarting {
		t.Fatalf("codex resume was archived after retryable start failure=%#v", got)
	}
}

func TestReconcileArchivesCodexResumeAfterRetryWindow(t *testing.T) {
	controller, machine, router := newControllerFixture(t)
	created := time.Now().Add(-2 * controller.startTimeout).UTC()
	session := domain.Session{
		ID: "resume-expired", NodeID: "alpha", OwnerID: 7, Backend: "codex", Workdir: t.TempDir(),
		ProviderSessionID: "invalid-provider", ProviderResume: true,
		State: domain.SessionLive, RuntimePhase: domain.RuntimeStarting,
		CreatedAt: created, LiveSinceAt: created, LastEventAt: created,
	}
	command, err := clusterstate.NewCommand("add-resume-expired", clusterstate.CommandAddSession, created, session)
	if err != nil {
		t.Fatal(err)
	}
	if result := machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	router.provisionErr = fmt.Errorf("%w: provider exited during startup", errProviderResumePending)
	controller.reconcile(context.Background())
	got := machine.State().Sessions[session.Ref().Key()]
	if got.State != domain.SessionArchived || got.ArchiveReason != domain.ArchiveResumeFailed {
		t.Fatalf("expired codex resume was not archived=%#v", got)
	}
}

func TestReconcileDegradesProvisionedCodexWhenProviderBindingTimesOut(t *testing.T) {
	controller, machine, router := newControllerFixture(t)
	router.discoveries = nil
	created := time.Now().Add(-5 * time.Minute).UTC()
	session := domain.Session{
		ID: "pending", NodeID: "alpha", OwnerID: 7, Backend: "codex", Workdir: t.TempDir(),
		State: domain.SessionLive, RuntimePhase: domain.RuntimeStarting,
		CreatedAt: created, LiveSinceAt: created, LastEventAt: created,
	}
	command, err := clusterstate.NewCommand("add-pending", clusterstate.CommandAddSession, created, session)
	if err != nil {
		t.Fatal(err)
	}
	if result := machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	controller.reconcile(context.Background())
	got := machine.State().Sessions[session.Ref().Key()]
	if !got.IsLive() || got.RuntimePhase != domain.RuntimeDegraded ||
		got.RuntimeIssue != domain.RuntimeIssueProviderHookUnavailable {
		t.Fatalf("missing provider binding=%#v", got)
	}

	router.discoveries = []transcript.Candidate{{ProviderSessionID: "provider-recovered"}}
	controller.reconcile(context.Background())
	healed := machine.State().Sessions[session.Ref().Key()]
	if healed.RuntimePhase != domain.RuntimeIdle || healed.RuntimeIssue != "" ||
		healed.ProviderSessionID != "provider-recovered" {
		t.Fatalf("late provider binding did not heal session=%#v", healed)
	}
}

func TestLocalAuthorizationRequiresExactVisibleOnlineNode(t *testing.T) {
	state := domain.NewState()
	for _, node := range []domain.Node{
		{ID: "target", Name: "Target", Status: domain.NodeOnline},
		{ID: "other", Name: "Other", Status: domain.NodeOnline},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeAccess(7, domain.RoleMember, "target"); err != nil {
		t.Fatal(err)
	}
	local := &Local{nodeID: "target", state: controllerPort{machine: clusterstate.NewMachine(state)}}
	if err := local.authorize(7, "target"); err != nil {
		t.Fatalf("visible target rejected: %v", err)
	}
	if err := local.authorize(7, "other"); err == nil {
		t.Fatal("different target accepted")
	}
	if err := local.authorize(8, "target"); err == nil {
		t.Fatal("unknown actor accepted")
	}
}
