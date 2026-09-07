package packaging_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeInstallChecksTmuxBeforeConfigOrInstallWrites(t *testing.T) {
	for _, script := range []string{"validate-install.sh", "install-release.sh"} {
		t.Run(script, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(bin, "uname"), []byte("#!/bin/sh\nprintf 'Linux\\n'\n"))
			for _, name := range []string{"bria", "bria-codex-adapter", "bria-claude-adapter"} {
				writeExecutable(t, filepath.Join(bin, name), []byte("#!/bin/sh\nprintf 'bria 1.0.0\\n'\n"))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			args := []string{script, bin, "1.0.0"}
			if script == "install-release.sh" {
				release := filepath.Join(root, "1.0.0")
				if err := os.Mkdir(release, 0700); err != nil {
					t.Fatal(err)
				}
				args = []string{script, "linux", "amd64", release, filepath.Join(root, "trust"), root, filepath.Join(root, "config")}
			}
			cmd := exec.CommandContext(ctx, "/bin/sh", args...)
			cmd.Env = []string{"PATH=" + bin}
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "tmux is required") {
				t.Fatalf("missing dependency was not rejected explicitly: %v %s", err, output)
			}
			if _, err := os.Stat(filepath.Join(root, "releases")); !os.IsNotExist(err) {
				t.Fatal("installer mutated release storage before dependency check")
			}
			if script == "validate-install.sh" {
				writeExecutable(t, filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nprintf 'tmux 3.7b\\n'\n"))
				cmd = exec.CommandContext(ctx, "/bin/sh", args...)
				cmd.Env = []string{"PATH=" + bin}
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("provided dependency rejected: %v %s", err, output)
				}
			}
		})
	}
}

func TestDockerRuntimeDeliversTmux(t *testing.T) {
	data, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	runtime := strings.SplitN(string(data), "FROM ${RUNTIME_IMAGE}", 2)
	if len(runtime) != 2 || !strings.Contains(runtime[1], "apk add --no-cache tmux") {
		t.Fatal("runtime image does not deliver native tmux dependency")
	}
}
