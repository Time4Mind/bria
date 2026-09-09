//go:build linux || darwin

package nativeterminal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/terminalbinding"
)

func TestPersistentAttachRejectsWrongIdentityAndReusedProcessProof(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root, err := os.MkdirTemp("/tmp", "bria-proof-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	binding := Binding{LogicalSessionID: "logical-proof", NativeSessionID: "native-proof", Provider: "fake", Workdir: root}
	term, err := Open(ctx, Config{Persistent: true, SocketDir: root, Workdir: root, Environment: os.Environ(), Command: []string{"/bin/cat"}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close(context.Background())
	if err := term.PersistBinding(ctx, root, binding); err != nil {
		t.Fatal(err)
	}
	if err := term.Detach(ctx); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Binding){
		func(b *Binding) { b.LogicalSessionID = "other" },
		func(b *Binding) { b.Workdir = filepath.Dir(root) },
		func(b *Binding) { b.NativeSessionID = "other" },
		func(b *Binding) { b.Provider = "other" },
	} {
		wrong := binding
		change(&wrong)
		if attached, err := AttachExisting(ctx, root, wrong); err == nil {
			_ = attached.Close(ctx)
			t.Fatal("wrong identity attached")
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "terminal-bindings"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			manifest = filepath.Join(root, "terminal-bindings", entry.Name())
		}
	}
	original, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.WriteFile(manifest, original, 0600)
	for name, mutate := range map[string]func(*terminalbinding.Record){
		"server birth": func(r *terminalbinding.Record) { r.ServerBirth = "reused" },
		"pane birth":   func(r *terminalbinding.Record) { r.PaneBirth = "reused" },
		"server pid":   func(r *terminalbinding.Record) { r.ServerPID = os.Getpid() },
		"pane pid":     func(r *terminalbinding.Record) { r.PanePID = os.Getpid() },
		"socket inode": func(r *terminalbinding.Record) { r.SocketInode++ },
		"nonce":        func(r *terminalbinding.Record) { r.Nonce = strings.Repeat("a", 64) },
		"pane id":      func(r *terminalbinding.Record) { r.PaneID = "%1" },
	} {
		t.Run(name, func(t *testing.T) {
			var record terminalbinding.Record
			if err := json.Unmarshal(original, &record); err != nil {
				t.Fatal(err)
			}
			mutate(&record)
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifest, data, 0600); err != nil {
				t.Fatal(err)
			}
			if attached, err := AttachExisting(ctx, root, binding); !errors.Is(err, ErrBindingMismatch) {
				if attached != nil {
					_ = attached.Detach(ctx)
				}
				t.Fatalf("unproven process attached: %v", err)
			}
		})
	}
	if err := os.WriteFile(manifest, original, 0600); err != nil {
		t.Fatal(err)
	}
	attached, err := AttachExisting(ctx, root, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := attached.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
