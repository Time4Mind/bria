package speechsetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type runnerStub struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (r *runnerStub) Run(_ context.Context, command string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{command}, args...))
	return nil, r.err
}

func (r *runnerStub) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([][]string, len(r.calls))
	for index, call := range r.calls {
		result[index] = append([]string(nil), call...)
	}
	return result
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
	if err != nil || status.Phase != PhaseMissing || len(runner.Calls()) != 0 {
		t.Fatalf("status=%#v err=%v calls=%#v", status, err, runner.Calls())
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
	if len(runner.Calls()) != 0 {
		t.Fatal("construction requested macOS permission")
	}
	status, err := manager.Start(context.Background(), Request{NodeID: "mac"})
	if err != nil || status.Phase != PhaseInstalling {
		t.Fatalf("start=%#v, %v", status, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(runner.Calls()) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	calls := runner.Calls()
	if len(calls) != 1 || calls[0][0] != helper || calls[0][1] != "--authorize" {
		t.Fatalf("authorization calls=%#v", calls)
	}
}

func TestConfiguredWhisperOverridesMacOSAppleDefault(t *testing.T) {
	manager, err := NewManager(Config{
		NodeID: "mac", OS: "darwin", Engine: "whisper", DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.engine(); got != "whisper" {
		t.Fatalf("speech setup engine=%q", got)
	}
}

func TestEngineChangeDropsStalePermissionFailure(t *testing.T) {
	directory := t.TempDir()
	ffmpeg := filepath.Join(directory, "ffmpeg")
	whisper := filepath.Join(directory, "whisper-cli")
	model := filepath.Join(directory, "model.bin")
	for _, path := range []string{ffmpeg, whisper, model} {
		if err := os.WriteFile(path, []byte("ready"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store := &Manager{config: Config{DataDir: directory}}
	store.persistStatus(Status{
		NodeID: "mac", Engine: "apple", Phase: PhasePermissionRequired,
		Detail: "old Apple permission", UpdatedAt: time.Now(),
	})
	manager, err := NewManager(Config{
		NodeID: "mac", OS: "darwin", Engine: "whisper", DataDir: directory,
		FFmpegCommand: ffmpeg, WhisperCommand: whisper, WhisperModel: model,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background(), Request{NodeID: "mac"})
	if err != nil || status.Engine != "whisper" || status.Phase != PhaseReady {
		t.Fatalf("status after engine change=%#v err=%v", status, err)
	}
	persisted, ok := manager.loadStatus()
	if !ok || persisted.Engine != "whisper" || persisted.Phase != PhaseReady {
		t.Fatalf("persisted status after engine change=%#v exists=%v", persisted, ok)
	}
}

func TestManagerPersistsTerminalFailureAcrossRestart(t *testing.T) {
	directory := t.TempDir()
	ffmpeg := filepath.Join(directory, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("stub"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &runnerStub{err: errors.New("clone failed")}
	config := Config{
		NodeID: "linux", OS: "linux", DataDir: directory,
		FFmpegCommand: ffmpeg, WhisperCommand: filepath.Join(directory, "missing-whisper"),
		WhisperModel: filepath.Join(directory, "model.bin"), Runner: runner,
	}
	manager, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), Request{NodeID: "linux"}); err != nil {
		t.Fatal(err)
	}
	var status Status
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, err = manager.Status(context.Background(), Request{NodeID: "linux"})
		if err != nil || status.Phase == PhaseFailed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil || status.Phase != PhaseFailed || status.Detail == "" {
		t.Fatalf("terminal status=%#v err=%v", status, err)
	}
	restarted, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restarted.Status(context.Background(), Request{NodeID: "linux"})
	if err != nil || restored.Phase != PhaseFailed || restored.Detail != status.Detail {
		t.Fatalf("restored status=%#v err=%v; want %#v", restored, err, status)
	}
}
