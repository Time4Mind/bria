package updateinstall_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"bria/internal/update"
	"bria/internal/updateflow"
	"bria/internal/updateinstall"
)

func TestPackagedCommandFactoryBindsSelectedStageOperationAndState(t *testing.T) {
	t.Parallel()
	factory := updateinstall.PackagedCommandFactory{Config: updateinstall.PackagedCommandConfig{
		Platform: "linux", Arch: "amd64", InstallExecutable: "/opt/bria/install-release.sh",
		RollbackExecutable: "/opt/bria/rollback-install.sh", VerifierExecutable: "/opt/bria/bria-releasemanifest",
		TrustFile: "/etc/bria/release-trust.json", InstallRoot: "/opt/bria", ConfigFile: "/etc/bria/config.json",
	}}
	request := updateflow.InstallRequest{
		OperationID: "install-op", NodeID: "node", FromVersion: "1.0.0", TargetVersion: "2.0.0", PriorState: "state-v1",
		Stage: validStageReceipt("node", "2.0.0"),
	}
	command, err := factory.InstallCommand(request)
	if err != nil {
		t.Fatalf("InstallCommand: %v", err)
	}
	want := updateinstall.Command{Executable: "/opt/bria/install-release.sh", Arguments: []string{
		"linux", "amd64", "2.0.0", "/staged/release", "/etc/bria/release-trust.json", "/opt/bria",
		"/etc/bria/config.json", "/opt/bria/bria-releasemanifest", "install-op", "node", "1.0.0", "state-v1",
	}}
	if !reflect.DeepEqual(command, want) {
		t.Fatalf("command = %#v, want %#v", command, want)
	}
}

func TestFileInstallLockSerializesDifferentFlowsForSameInstallRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := updateinstall.OpenFileInstallLock(root)
	if err != nil {
		t.Fatalf("OpenFileInstallLock: %v", err)
	}
	entered := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		done <- lock.WithLock(context.Background(), func() error {
			entered <- "first"
			<-release
			return nil
		})
	}()
	if got := <-entered; got != "first" {
		t.Fatalf("first entry = %q", got)
	}
	go func() {
		done <- lock.WithLock(context.Background(), func() error {
			entered <- "second"
			return nil
		})
	}()
	select {
	case got := <-entered:
		t.Fatalf("overlapping entry = %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-entered:
		if got != "second" {
			t.Fatalf("second entry = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second flow did not enter after release")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPackagedHealthReaderRequiresScriptAndObservableReadiness(t *testing.T) {
	t.Parallel()
	runner := &fakeCommandRunner{}
	want := update.HealthReceipt{
		NodeID: "node", RunningVersion: "2.0.0", Started: true, StateReadable: true,
		ProvidersAvailable: true, CoordinatorConnected: true, SessionsAvailable: true, ProbeSucceeded: true,
	}
	reader := updateinstall.PackagedHealthReader{
		Config: updateinstall.PackagedHealthConfig{
			Platform: "linux", Executable: "/opt/bria/postflight.sh", InstallRoot: "/opt/bria",
			ConfigFile: "/etc/bria/config.json", ServiceFile: "/etc/systemd/system/bria.service",
		},
		Runner: runner, Observer: fixedReadinessObserver{receipt: want},
	}
	receipt, err := reader.ReadHealth(context.Background(), "node", "2.0.0")
	if err != nil {
		t.Fatalf("ReadHealth: %v", err)
	}
	if receipt != want {
		t.Fatalf("receipt = %#v, want %#v", receipt, want)
	}
	wantCommand := updateinstall.Command{Executable: "/opt/bria/postflight.sh", Arguments: []string{
		"linux", "/opt/bria", "2.0.0", "/etc/bria/config.json", "/etc/systemd/system/bria.service",
	}}
	if !reflect.DeepEqual(runner.commands, []updateinstall.Command{wantCommand}) {
		t.Fatalf("commands = %#v", runner.commands)
	}
}

type fixedReadinessObserver struct{ receipt update.HealthReceipt }

func (o fixedReadinessObserver) ObserveHealth(context.Context, string, string) (update.HealthReceipt, error) {
	return o.receipt, nil
}

func TestFileInstallStateReaderReadsManagedVersionAndExactFingerprint(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "releases", "2.0.0"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("releases/2.0.0", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	fingerprint := filepath.Join(root, "state.fingerprint")
	if err := os.WriteFile(fingerprint, []byte("state-v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := updateinstall.OpenFileInstallStateReader(root, fingerprint)
	if err != nil {
		t.Fatalf("OpenFileInstallStateReader: %v", err)
	}
	state, err := reader.ReadInstalledState(context.Background(), "node")
	if err != nil {
		t.Fatalf("ReadInstalledState: %v", err)
	}
	if state.Version != "2.0.0" || state.StateFingerprint != "state-v1" {
		t.Fatalf("state = %#v", state)
	}
}

func TestFileOperationProofVerifierBindsReceiptAndRunsIndependentInstalledVerification(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	version := "2.0.0"
	if err := os.MkdirAll(filepath.Join(root, "releases", version), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "release-receipts", version), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "update-operation-receipts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("releases/"+version, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	proof := updateinstall.OperationProof{
		Kind: "install", OperationID: "install-op", NodeID: "node", FromVersion: "1.0.0", TargetVersion: version, State: "state-v1",
	}
	content := "install-op\nnode\n1.0.0\n2.0.0\nstate-v1\n"
	if err := os.WriteFile(filepath.Join(root, "update-operation-receipts", proof.OperationID), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{}
	verifier := updateinstall.FileOperationProofVerifier{
		Platform: "linux", Arch: "amd64", InstallRoot: root, TrustFile: "/etc/bria/trust.json",
		VerifierExecutable: "/usr/local/libexec/bria-releasemanifest", Runner: runner,
	}
	if err := verifier.VerifyOperation(context.Background(), proof); err != nil {
		t.Fatalf("VerifyOperation: %v", err)
	}
	if len(runner.commands) != 1 || runner.commands[0].Executable != verifier.VerifierExecutable ||
		len(runner.commands[0].Arguments) == 0 || runner.commands[0].Arguments[0] != "verify-installed" {
		t.Fatalf("commands = %#v", runner.commands)
	}
	receipt := filepath.Join(root, "update-operation-receipts", proof.OperationID)
	pending := filepath.Join(filepath.Dir(receipt), ".pending-"+proof.OperationID)
	if err := os.Rename(receipt, pending); err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyOperation(context.Background(), proof); err != nil {
		t.Fatalf("VerifyOperation pending recovery: %v", err)
	}
	if _, err := os.Lstat(receipt); err != nil {
		t.Fatalf("confirmed receipt after recovery: %v", err)
	}
}
