package speechsetup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type runnerStub struct {
	calls [][]string
	err   error
}

func (r *runnerStub) Run(_ context.Context, command string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{command}, args...))
	return nil, r.err
}

func TestManagerNeverStartsInstallationDuringConstructionOrStatus(t *testing.T) {
	runner := &runnerStub{}
	manager, err := NewManager(Config{
		NodeID: "n", OS: "linux", DataDir: t.TempDir(), FFmpegCommand: "missing-ffmpeg",
		WhisperCommand: "missing-whisper", WhisperModel: filepath.Join(t.TempDir(), "model.bin"),
		Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background(), Request{NodeID: "n"})
	if err != nil || status.Phase != PhaseMissing || len(runner.calls) != 0 {
		t.Fatalf("status=%#v err=%v calls=%#v", status, err, runner.calls)
	}
}

func TestAppleSetupRequestsAuthorizationOnlyAfterExplicitStart(t *testing.T) {
	directory := t.TempDir()
	helper := filepath.Join(directory, "speech", "bin", "bria-apple-speech")
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &runnerStub{}
	manager, err := NewManager(Config{
		NodeID: "mac", OS: "darwin", DataDir: directory, AppleCommand: helper, Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("construction requested macOS permission")
	}
	status, err := manager.Start(context.Background(), Request{NodeID: "mac"})
	if err != nil || status.Phase != PhaseInstalling {
		t.Fatalf("start=%#v, %v", status, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(runner.calls) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(runner.calls) != 1 || runner.calls[0][0] != helper || runner.calls[0][1] != "--authorize" {
		t.Fatalf("authorization calls=%#v", runner.calls)
	}
}
