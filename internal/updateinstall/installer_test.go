package updateinstall_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"bria/internal/update"
	"bria/internal/updateflow"
	"bria/internal/updateinstall"
)

func TestPackagedInstallerUsesDirectCommandAndReturnsRereadExactReceipt(t *testing.T) {
	t.Parallel()
	request := updateflow.InstallRequest{
		OperationID: "install-op", NodeID: "executor", FromVersion: "1.0.0",
		TargetVersion: "2.0.0", PriorState: "state-v1",
		Stage: validStageReceipt("executor", "2.0.0"),
	}
	command := updateinstall.Command{Executable: "/package/install-release", Arguments: []string{"fixed", "/staged/release"}}
	commands := &fakeCommandFactory{install: command}
	runner := &fakeCommandRunner{}
	state := &sequenceStateReader{states: []updateinstall.InstalledState{
		{Version: "1.0.0", StateFingerprint: "state-v1"},
		{Version: "2.0.0", StateFingerprint: "state-v1"},
	}}
	installer := updateinstall.PackagedInstaller{Commands: commands, Runner: runner, State: state, Lock: immediateInstallLock{}, Proof: fixedOperationProof{}}

	receipt, err := installer.Install(context.Background(), request)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	wantReceipt := updateflow.InstallReceipt{
		OperationID: request.OperationID, NodeID: request.NodeID,
		RunningVersion: request.TargetVersion, State: request.PriorState, Applied: true,
	}
	if receipt != wantReceipt {
		t.Fatalf("receipt = %#v, want %#v", receipt, wantReceipt)
	}
	if !reflect.DeepEqual(runner.commands, []updateinstall.Command{command}) {
		t.Fatalf("commands = %#v", runner.commands)
	}
}

func TestPackagedInstallerReconcilesSuccessfulInstallAfterAmbiguousRunnerError(t *testing.T) {
	t.Parallel()
	request := updateflow.InstallRequest{
		OperationID: "install-op", NodeID: "executor", FromVersion: "1.0.0",
		TargetVersion: "2.0.0", PriorState: "state-v1",
		Stage: validStageReceipt("executor", "2.0.0"),
	}
	installer := updateinstall.PackagedInstaller{
		Commands: &fakeCommandFactory{install: updateinstall.Command{Executable: "/package/install-release"}},
		Runner:   &fakeCommandRunner{err: errors.New("ambiguous transport failure")},
		Lock:     immediateInstallLock{},
		Proof:    fixedOperationProof{},
		State: &sequenceStateReader{states: []updateinstall.InstalledState{
			{Version: "1.0.0", StateFingerprint: "state-v1"},
			{Version: "2.0.0", StateFingerprint: "state-v1"},
		}},
	}

	receipt, err := installer.Install(context.Background(), request)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if receipt.OperationID != request.OperationID || receipt.RunningVersion != request.TargetVersion || receipt.State != request.PriorState {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func validStageReceipt(nodeID, version string) updateflow.StageReceipt {
	return updateflow.StageReceipt{
		OperationID: "stage-op", NodeID: nodeID, Version: version, Reference: "/staged/release",
		Artifact: update.Artifact{
			Name: "bria-linux-amd64.tar.gz", Platform: "linux", Arch: "amd64", Size: 1,
			SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
}

func TestPackagedInstallerRejectsStageReceiptForAnotherNode(t *testing.T) {
	t.Parallel()
	request := updateflow.InstallRequest{
		OperationID: "install-op", NodeID: "executor", FromVersion: "1.0.0",
		TargetVersion: "2.0.0", PriorState: "state-v1",
		Stage: updateflow.StageReceipt{
			OperationID: "stage-op", NodeID: "other", Version: "2.0.0", Reference: "/staged/release",
			Artifact: update.Artifact{Name: "bria-linux-amd64.tar.gz", Platform: "linux", Arch: "amd64", Size: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
	}
	installer := updateinstall.PackagedInstaller{
		Commands: &fakeCommandFactory{}, Runner: &fakeCommandRunner{}, State: &sequenceStateReader{}, Lock: immediateInstallLock{}, Proof: fixedOperationProof{},
	}

	if _, err := installer.Install(context.Background(), request); !errors.Is(err, updateinstall.ErrInvalidInstaller) {
		t.Fatalf("Install error = %v, want ErrInvalidInstaller", err)
	}
}

func TestPackagedInstallerDoesNotCreditPreexistingTargetWithoutExactOperationProof(t *testing.T) {
	t.Parallel()
	request := updateflow.InstallRequest{
		OperationID: "install-op", NodeID: "executor", FromVersion: "1.0.0", TargetVersion: "2.0.0", PriorState: "state-v1",
		Stage: validStageReceipt("executor", "2.0.0"),
	}
	installer := updateinstall.PackagedInstaller{
		Commands: &fakeCommandFactory{}, Runner: &fakeCommandRunner{}, Lock: immediateInstallLock{},
		State: &sequenceStateReader{states: []updateinstall.InstalledState{{Version: "2.0.0", StateFingerprint: "state-v1"}}},
		Proof: fixedOperationProof{err: errors.New("missing exact proof")},
	}
	if _, err := installer.Install(context.Background(), request); err == nil {
		t.Fatal("Install credited a preexisting target without operation proof")
	}
}

func TestPackagedInstallerRecordsVerifiedNoopRollbackWhenPriorVersionNeverMoved(t *testing.T) {
	t.Parallel()
	request := updateflow.RollbackRequest{
		OperationID: "rollback-op", NodeID: "executor", FromVersion: "2.0.0", TargetVersion: "1.0.0", TargetState: "state-v1",
	}
	command := updateinstall.Command{Executable: "/package/rollback-install"}
	runner := &fakeCommandRunner{}
	installer := updateinstall.PackagedInstaller{
		Commands: &fakeCommandFactory{rollback: command}, Runner: runner, Lock: immediateInstallLock{},
		State: &sequenceStateReader{states: []updateinstall.InstalledState{
			{Version: "1.0.0", StateFingerprint: "state-v1"}, {Version: "1.0.0", StateFingerprint: "state-v1"},
		}},
		Proof: &sequenceOperationProof{errors: []error{errors.New("receipt absent"), nil}},
	}
	receipt, err := installer.Rollback(context.Background(), request)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if receipt.RunningVersion != request.TargetVersion || !reflect.DeepEqual(runner.commands, []updateinstall.Command{command}) {
		t.Fatalf("receipt/commands = %#v / %#v", receipt, runner.commands)
	}
}

type immediateInstallLock struct{}

func (immediateInstallLock) WithLock(_ context.Context, action func() error) error { return action() }

type fixedOperationProof struct{ err error }

func (p fixedOperationProof) VerifyOperation(context.Context, updateinstall.OperationProof) error {
	return p.err
}

type sequenceOperationProof struct {
	errors []error
	index  int
}

func (p *sequenceOperationProof) VerifyOperation(context.Context, updateinstall.OperationProof) error {
	err := p.errors[p.index]
	p.index++
	return err
}

type fakeCommandFactory struct {
	install  updateinstall.Command
	rollback updateinstall.Command
}

func (f *fakeCommandFactory) InstallCommand(updateflow.InstallRequest) (updateinstall.Command, error) {
	return f.install, nil
}

func (f *fakeCommandFactory) RollbackCommand(updateflow.RollbackRequest) (updateinstall.Command, error) {
	return f.rollback, nil
}

type fakeCommandRunner struct {
	commands []updateinstall.Command
	err      error
}

func (r *fakeCommandRunner) Run(_ context.Context, command updateinstall.Command) error {
	r.commands = append(r.commands, command)
	return r.err
}

type sequenceStateReader struct {
	states []updateinstall.InstalledState
	index  int
}

func (r *sequenceStateReader) ReadInstalledState(context.Context, string) (updateinstall.InstalledState, error) {
	state := r.states[r.index]
	r.index++
	return state, nil
}
