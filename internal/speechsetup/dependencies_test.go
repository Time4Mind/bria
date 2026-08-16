package speechsetup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/systemdeps"
)

func TestWhisperSetupRequestsAndWaitsForSystemFFmpeg(t *testing.T) {
	directory := t.TempDir()
	ffmpeg := filepath.Join(directory, "bin", "ffmpeg")
	whisper := filepath.Join(directory, "bin", "whisper-cli")
	model := filepath.Join(directory, "model.bin")
	requests := filepath.Join(directory, "system-deps", "requests")
	results := filepath.Join(directory, "runtime")
	if err := os.MkdirAll(filepath.Dir(whisper), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(results, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(whisper, []byte("stub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Config{
		NodeID: "linux", OS: "linux", DataDir: directory,
		FFmpegCommand: ffmpeg, WhisperCommand: whisper, WhisperModel: model,
		Dependencies: systemdeps.Config{RequestDir: requests, ResultDir: results},
	})
	if err != nil {
		t.Fatal(err)
	}
	helperDone := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			entries, readErr := os.ReadDir(requests)
			if readErr != nil || len(entries) == 0 {
				time.Sleep(time.Millisecond)
				continue
			}
			identity := strings.TrimSuffix(entries[0].Name(), ".request")
			if mkdirErr := os.MkdirAll(filepath.Dir(ffmpeg), 0o700); mkdirErr != nil {
				helperDone <- mkdirErr
				return
			}
			if writeErr := os.WriteFile(ffmpeg, []byte("stub"), 0o700); writeErr != nil {
				helperDone <- writeErr
				return
			}
			helperDone <- os.WriteFile(
				filepath.Join(results, identity+".result"), []byte("ready\n"), 0o600,
			)
			return
		}
		helperDone <- context.DeadlineExceeded
	}()
	if _, err := manager.Start(context.Background(), Request{NodeID: "linux"}); err != nil {
		t.Fatal(err)
	}
	if err := <-helperDone; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := manager.Status(context.Background(), Request{NodeID: "linux"})
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if status.Phase == PhaseReady {
			return
		}
		if status.Phase == PhaseFailed {
			t.Fatalf("automatic dependency installation failed: %#v", status)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("speech setup did not become ready")
}
