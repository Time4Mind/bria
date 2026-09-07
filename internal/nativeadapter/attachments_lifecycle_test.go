package nativeadapter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/runtimeprotocol"
)

func TestClaudePhotoRunCleansPrivateMediaOnCloseErrorAndCancellation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native terminal Linux pilot")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	for _, ending := range []string{"close", "error", "cancel"} {
		t.Run(ending, func(t *testing.T) {
			root, err := os.MkdirTemp("", "bria-photo-test-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			t.Setenv("TMPDIR", root)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
			var data bytes.Buffer
			if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(root, "photo")
			if err := os.WriteFile(original, data.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			fixture := filepath.Join(root, "fixture-cli")
			if err := os.WriteFile(fixture, []byte("#!/bin/sh\nprintf 'Claude Code\\n❯\\n'\nwhile IFS= read -r line; do :; done\n"), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			done := make(chan error, 1)
			go func() {
				err := Run(ctx, inR, outW, Config{Provider: domain.ProviderClaude, Command: []string{fixture}, Workdir: root, ResumeID: fixtureSession, StateDir: filepath.Join(root, "state"), Environment: os.Environ()})
				_ = outW.Close()
				done <- err
			}()
			frames := make(chan []byte, 8)
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				scanner := bufio.NewScanner(outR)
				for scanner.Scan() {
					frames <- append([]byte(nil), scanner.Bytes()...)
				}
			}()
			defer func() {
				cancel()
				_ = inW.Close()
				_ = outR.Close()
				select {
				case <-done:
				case <-time.After(6 * time.Second):
					t.Error("native photo fixture cleanup timeout")
				}
				<-readDone
			}()
			select {
			case frame := <-frames:
				m, err := runtimeprotocol.DecodeAdapterLine(frame, runtimeprotocol.Limits{})
				if err != nil || m.Type != runtimeprotocol.TypeReady {
					t.Fatalf("not ready: %v %s", err, frame)
				}
			case err := <-done:
				done <- err
				t.Fatalf("early exit: %v", err)
			case <-ctx.Done():
				t.Fatal("ready timeout")
			}
			send := func(m runtimeprotocol.ParentMessage) {
				m.Protocol = 1
				b, err := runtimeprotocol.EncodeParentLine(m, runtimeprotocol.Limits{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := inW.Write(b); err != nil {
					t.Fatal(err)
				}
			}
			send(runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeSubmit, RequestID: "r-photo", MessageID: "m-photo", Text: "inspect image", Attachments: []runtimeprotocol.LocalAttachment{{Path: original, Size: int64(data.Len()), SHA256: fmt.Sprintf("%x", sha256.Sum256(data.Bytes()))}}})
			for {
				matches, _ := filepath.Glob(filepath.Join(root, "bria-native-media-*", "*.png"))
				if len(matches) == 1 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("media was not staged")
				case <-time.After(10 * time.Millisecond):
				}
			}
			switch ending {
			case "cancel":
				cancel()
			case "close":
				send(runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
			case "error":
				send(runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeSubmit, RequestID: "duplicate", MessageID: "duplicate", Text: "second submit while active"})
			}
			select {
			case err := <-done:
				done <- err
				if ending == "close" && err != nil {
					t.Fatal(err)
				}
				if ending != "close" && err == nil {
					t.Fatal("expected error/cancellation")
				}
			case <-time.After(6 * time.Second):
				t.Fatal("adapter did not close")
			}
			matches, _ := filepath.Glob(filepath.Join(root, "bria-native-media-*"))
			if len(matches) != 0 {
				t.Fatal("private staged media leaked")
			}
			if _, err := os.Stat(original); err != nil {
				t.Fatal("custody original removed")
			}
		})
	}
}
