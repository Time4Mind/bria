package nativeadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"bria/internal/domain"
	"bria/internal/nativeterminal"
	"bria/internal/runtimeprotocol"
)

func TestPersistentPhotoSurvivesDetachAndClosesWithExactCLI(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	fixture := filepath.Join(root, "fake-claude")
	if err := os.WriteFile(fixture, []byte("#!/bin/sh\nprintf 'Claude Code\\n❯\\n'\nwhile IFS= read -r line; do printf '%s\\n' \"$line\"; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "original.png")
	if err := os.WriteFile(original, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	attachment := runtimeprotocol.LocalAttachment{Path: original, Size: int64(data.Len()), SHA256: fmt.Sprintf("%x", sha256.Sum256(data.Bytes()))}
	config := Config{Persistent: true, LogicalSessionID: "logical-photo", Provider: domain.ProviderClaude,
		Command: []string{fixture}, Workdir: root, ResumeID: fixtureSession, StateDir: filepath.Join(root, "state"), Environment: os.Environ()}
	binding := nativeterminal.Binding{LogicalSessionID: config.LogicalSessionID, NativeSessionID: fixtureSession, Provider: "claude", Workdir: root}
	defer func() {
		if term, err := nativeterminal.AttachExisting(context.Background(), config.StateDir, binding); err == nil {
			_ = term.Close(context.Background())
		}
	}()
	first := startPersistentAdapterFixture(t, config)
	if ready := first.receive(t); ready.Type != runtimeprotocol.TypeReady {
		t.Fatalf("first Ready: %+v", ready)
	}
	first.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeSubmit, RequestID: "photo", MessageID: "photo-input", Text: "inspect", Attachments: []runtimeprotocol.LocalAttachment{attachment}})
	first.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeNativeControl, RequestID: "screen"})
	if screen := first.receive(t); screen.Type != runtimeprotocol.TypeNativeSnapshot {
		t.Fatalf("photo not delivered to CLI: %+v", screen)
	}
	stopPersistentAdapterEOF(t, first)
	paths, err := filepath.Glob(filepath.Join(config.StateDir, "bria-native-media-*", attachment.SHA256+".png"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("detach lost staged photo: %v %v", paths, err)
	}
	staged, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(staged, data.Bytes()) {
		t.Fatal("detach changed photo bytes")
	}
	config.AttachOnly, config.Command = true, nil
	second := startPersistentAdapterFixture(t, config)
	if ready := second.receive(t); ready.Type != runtimeprotocol.TypeReady {
		t.Fatalf("attach Ready: %+v", ready)
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatal("new observer lost prior staged photo")
	}
	second.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
	if closed := second.receive(t); closed.Type != runtimeprotocol.TypeClosed || closed.ProviderSessionID != fixtureSession {
		t.Fatalf("physical Close ack missing: %+v", closed)
	}
	stopPersistentAdapterEOF(t, second)
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("explicit Close did not release staged photo: %v", err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatal("source custody was deleted")
	}
}
