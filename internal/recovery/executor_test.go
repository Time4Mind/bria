package recovery_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/recovery"
)

type machinePort struct{ machine *clusterstate.Machine }

func (p machinePort) State() *domain.State { return p.machine.State() }
func (p machinePort) Apply(_ context.Context, command clusterstate.Command) (clusterstate.Result, error) {
	return p.machine.Apply(command), nil
}

type flakyMachinePort struct {
	machine      *clusterstate.Machine
	failComplete bool
}

func (p *flakyMachinePort) State() *domain.State { return p.machine.State() }
func (p *flakyMachinePort) Apply(_ context.Context, command clusterstate.Command) (clusterstate.Result, error) {
	if command.Kind == clusterstate.CommandCompleteBootRecovery && p.failComplete {
		p.failComplete = false
		return clusterstate.Result{}, errors.New("temporary consensus failure")
	}
	return p.machine.Apply(command), nil
}

type runtimeStub struct {
	err          error
	calls        int
	operationIDs []string
}

func (r *runtimeStub) Resume(_ context.Context, _ domain.Session, operationID string) error {
	r.calls++
	r.operationIDs = append(r.operationIDs, operationID)
	if operationID == "" {
		return errors.New("missing operation id")
	}
	return r.err
}

func recoveringMachine(t *testing.T) (*clusterstate.Machine, domain.SessionRef) {
	t.Helper()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", BootID: "boot-2"}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{
		ID: "s", NodeID: "node", OwnerID: 1, Name: "S", Backend: "codex",
		ProviderSessionID: "provider", State: domain.SessionRecovering,
		CreatedAt: time.Unix(1, 0), LastEventAt: time.Unix(2, 0),
	}
	state.Sessions[session.Ref().Key()] = session
	return clusterstate.NewMachine(state), session.Ref()
}

func TestExecutorCommitsRecoveryOnlyAfterRuntimeSuccess(t *testing.T) {
	machine, ref := recoveringMachine(t)
	runtime := &runtimeStub{}
	port := machinePort{machine}
	executor, err := recovery.NewExecutor("node", port, port, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Run(context.Background(), []domain.SessionRef{ref}); err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 1 || machine.State().Sessions[ref.Key()].State != domain.SessionActive {
		t.Fatalf("calls=%d session=%#v", runtime.calls, machine.State().Sessions[ref.Key()])
	}
}

func TestExecutorArchivesFailedResume(t *testing.T) {
	machine, ref := recoveringMachine(t)
	runtime := &runtimeStub{err: errors.New("provider unavailable")}
	port := machinePort{machine}
	executor, err := recovery.NewExecutor("node", port, port, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Run(context.Background(), []domain.SessionRef{ref}); err == nil {
		t.Fatal("runtime failure not returned")
	}
	session := machine.State().Sessions[ref.Key()]
	if session.State != domain.SessionArchived || session.ArchiveReason != domain.ArchiveResumeFailed {
		t.Fatalf("failed recovery state=%s", machine.State().Sessions[ref.Key()].State)
	}
}

func TestExecutorRejectsRecoveryForAnotherNode(t *testing.T) {
	machine, ref := recoveringMachine(t)
	runtime := &runtimeStub{}
	port := machinePort{machine}
	executor, err := recovery.NewExecutor("other", port, port, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Run(context.Background(), []domain.SessionRef{ref}); !errors.Is(err, domain.ErrAccessDenied) {
		t.Fatalf("cross-node recovery error=%v", err)
	}
	if runtime.calls != 0 {
		t.Fatalf("cross-node runtime calls=%d", runtime.calls)
	}
}

func TestExecutorRetryUsesSameRuntimeOperationID(t *testing.T) {
	machine, ref := recoveringMachine(t)
	runtime := &runtimeStub{}
	port := &flakyMachinePort{machine: machine, failComplete: true}
	executor, err := recovery.NewExecutor("node", port, port, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Run(context.Background(), []domain.SessionRef{ref}); err == nil {
		t.Fatal("temporary consensus failure not returned")
	}
	if err := executor.Run(context.Background(), []domain.SessionRef{ref}); err != nil {
		t.Fatal(err)
	}
	if len(runtime.operationIDs) != 2 || runtime.operationIDs[0] != runtime.operationIDs[1] {
		t.Fatalf("runtime operation IDs=%#v", runtime.operationIDs)
	}
}
