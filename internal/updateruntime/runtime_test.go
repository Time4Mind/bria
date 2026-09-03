package updateruntime_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/update"
	"bria/internal/updateflow"
	"bria/internal/updateinstall"
	"bria/internal/updateruntime"
)

func TestRuntimeReopensDurablePreparedFlowAndCompletesWithConcreteAdapters(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	releaseDirectory := filepath.Join(root, "release")
	if err := os.Mkdir(releaseDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("packaged release")
	manifest, err := update.BuildManifest("2.0.0", []update.ArtifactSource{{
		Name: "bria-linux-amd64.tar.gz", Platform: "linux", Arch: "amd64", Content: bytes.NewReader(payload),
	}})
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signed, err := update.SignManifest(manifest, "release-key", privateKey)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	manifestPath := filepath.Join(releaseDirectory, "manifest.json")
	if err := os.WriteFile(manifestPath, signed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDirectory, manifest.Artifacts[0].Name), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	stateReader := &runtimeStateReader{states: []updateinstall.InstalledState{
		{Version: "1.0.0", StateFingerprint: "state-v1"},
		{Version: "2.0.0", StateFingerprint: "state-v1"},
		{Version: "2.0.0", StateFingerprint: "state-v1"},
	}}
	config := updateruntime.RuntimeConfig{
		Source: updateruntime.SourceConfig{
			ManifestURL: "file://" + manifestPath, MaxManifestBytes: 64 << 10, MaxArtifactBytes: 1 << 20,
		},
		StageDirectory: filepath.Join(root, "stage"), StateDirectory: filepath.Join(root, "state"),
		MaximumStageBytes: 1 << 20, TrustedKeys: update.TrustedKeys{"release-key": publicKey},
		Commands: runtimeCommandFactory{command: updateinstall.Command{Executable: "/package/install-release"}},
		Runner:   runtimeCommandRunner{}, State: stateReader,
		InstallLock:    runtimeInstallLock{},
		OperationProof: runtimeOperationProof{},
		Integrity:      runtimeIntegrity{},
		Health: runtimeHealthReader{receipt: update.HealthReceipt{
			NodeID: "coordinator", RunningVersion: "2.0.0", Started: true, StateReadable: true,
			ProvidersAvailable: true, SessionsAvailable: true, ProbeSucceeded: true,
		}},
	}
	first, err := updateruntime.OpenRuntime(config)
	if err != nil {
		t.Fatalf("OpenRuntime first: %v", err)
	}
	request := updateflow.Request{FlowID: "durable-flow", Targets: []updateflow.Target{{
		Node:     update.Node{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline},
		Platform: "linux", Arch: "amd64", PriorState: "state-v1",
	}}}
	if _, err := first.Service.Prepare(context.Background(), request); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	reopened, err := updateruntime.OpenRuntime(config)
	if err != nil {
		t.Fatalf("OpenRuntime reopened: %v", err)
	}
	completed, err := reopened.Service.Run(context.Background(), request.FlowID)
	if err != nil {
		t.Fatalf("Run after reopen: %v", err)
	}
	if completed.Phase != updateflow.PhaseCompleted || completed.Rollout.Nodes()[0].CurrentVersion != "2.0.0" {
		t.Fatalf("completed = %#v", completed)
	}
}

type runtimeCommandFactory struct{ command updateinstall.Command }

func (f runtimeCommandFactory) InstallCommand(updateflow.InstallRequest) (updateinstall.Command, error) {
	return f.command, nil
}

func (f runtimeCommandFactory) RollbackCommand(updateflow.RollbackRequest) (updateinstall.Command, error) {
	return f.command, nil
}

type runtimeCommandRunner struct{}

func (runtimeCommandRunner) Run(context.Context, updateinstall.Command) error { return nil }

type runtimeInstallLock struct{}

func (runtimeInstallLock) WithLock(_ context.Context, action func() error) error { return action() }

type runtimeOperationProof struct{}

func (runtimeOperationProof) VerifyOperation(context.Context, updateinstall.OperationProof) error {
	return nil
}

type runtimeIntegrity struct{}

func (runtimeIntegrity) VerifyCurrent(context.Context, string, string) error { return nil }

type runtimeStateReader struct {
	states []updateinstall.InstalledState
	index  int
}

func (r *runtimeStateReader) ReadInstalledState(context.Context, string) (updateinstall.InstalledState, error) {
	state := r.states[r.index]
	r.index++
	return state, nil
}

type runtimeHealthReader struct{ receipt update.HealthReceipt }

func (r runtimeHealthReader) ReadHealth(context.Context, string, string) (update.HealthReceipt, error) {
	return r.receipt, nil
}
