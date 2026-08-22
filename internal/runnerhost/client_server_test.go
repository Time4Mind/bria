package runnerhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerbinding"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func TestClientRunsOnlyInServerEnvironment(t *testing.T) {
	// Darwin limits Unix-domain socket paths to roughly 104 bytes. Go's test
	// temp path includes the full test name and can exceed that before Serve is
	// reached, so keep the socket root deliberately short.
	root, err := os.MkdirTemp("", "bria-runner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "r.sock")
	bindings, err := providerbinding.NewStore(filepath.Join(root, "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServerWithBindings(runtimehost.ExecCommandRunner{}, bindings)
	if err != nil {
		t.Fatal(err)
	}
	errorsOut := make(chan error, 1)
	go func() { errorsOut <- server.Serve(socket) }()
	waitForSocket(t, socket, errorsOut)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		if err := <-errorsOut; err != nil {
			t.Errorf("serve: %v", err)
		}
	})

	client, err := NewClient(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	inspect, err := client.Inspect(ctx)
	if err != nil || inspect.ProtocolVersion != ProtocolVersion {
		t.Fatalf("inspect=%+v err=%v", inspect, err)
	}
	result, err := client.RunInput(ctx, []byte("payload"), "sh", "-c", "read value; printf '%s' \"$value\"")
	if err != nil || result.ExitCode != 0 || string(result.Stdout) != "payload" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	record := providerbinding.Record{
		NodeID: "node", SessionID: "session", ProviderSessionID: "019fffe8-02ee-7aa1-b6cf-eed13a005482",
		Workdir: root, TmuxSession: "bria", TmuxWindow: "window", RuntimeGeneration: 2,
		UpdatedAt: time.Now().UTC(),
	}
	if err := bindings.Put(record); err != nil {
		t.Fatal(err)
	}
	got, found, err := client.Lookup(ref, root)
	if err != nil || !found || got.ProviderSessionID != record.ProviderSessionID {
		t.Fatalf("remote binding=%#v found=%t err=%v", got, found, err)
	}
	snapshot, err := client.Snapshot()
	if err != nil || len(snapshot) != 1 {
		t.Fatalf("remote snapshot=%#v err=%v", snapshot, err)
	}
	if err := client.DeleteIfGeneration(ref, 2); err != nil {
		t.Fatal(err)
	}
	if _, found, err := client.LookupRef(ref); err != nil || found {
		t.Fatalf("remote binding survived delete: found=%t err=%v", found, err)
	}
}

func TestServerRefusesToReplaceRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.sock")
	if err := os.WriteFile(path, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(runtimehost.ExecCommandRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Serve(path); err == nil {
		t.Fatal("regular file was replaced")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "do not replace" {
		t.Fatalf("regular file changed: %q %v", content, err)
	}
}

func waitForSocket(t *testing.T, socket string, serveErrors <-chan error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case err := <-serveErrors:
			t.Fatalf("start runner server: %v", err)
		default:
		}
		info, err := os.Stat(socket)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("runner socket was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
