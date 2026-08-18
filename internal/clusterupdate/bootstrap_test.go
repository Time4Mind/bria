package clusterupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type bootstrapManagerFixture struct {
	manager          *Manager
	activation       string
	installRoot      string
	artifactRequests *atomic.Int64
	watchdogs        *atomic.Int64
}

func TestEnsureCompatibleUpdatesBeforeClusterJoin(t *testing.T) {
	fixture := newBootstrapManagerFixture(t, 2, 2)
	result, err := fixture.manager.EnsureCompatible(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Required || result.ManifestVersion != "v2" ||
		result.ActivationPath != fixture.activation {
		t.Fatalf("compatibility result=%#v", result)
	}
	resolved, err := filepath.EvalSymlinks(fixture.activation)
	if err != nil || resolved == fixture.activation {
		t.Fatalf("activation target=%q err=%v", resolved, err)
	}
	if fixture.artifactRequests.Load() != 1 || fixture.watchdogs.Load() != 1 {
		t.Fatalf("artifact requests=%d watchdogs=%d",
			fixture.artifactRequests.Load(), fixture.watchdogs.Load())
	}
}

func TestEnsureCompatibleDoesNotForceUpdateWithinProtocolFloor(t *testing.T) {
	fixture := newBootstrapManagerFixture(t, 1, 2)
	result, err := fixture.manager.EnsureCompatible(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Required || result.ActivationPath != "" {
		t.Fatalf("compatibility result=%#v", result)
	}
	if fixture.artifactRequests.Load() != 0 || fixture.watchdogs.Load() != 0 {
		t.Fatalf("compatible node downloaded update: artifacts=%d watchdogs=%d",
			fixture.artifactRequests.Load(), fixture.watchdogs.Load())
	}
	content, err := os.ReadFile(fixture.activation)
	if err != nil || string(content) != "old" {
		t.Fatalf("compatible activation changed: %q %v", content, err)
	}
}

func TestEnsureCompatibleRejectsArtifactBelowManifestFloor(t *testing.T) {
	fixture := newBootstrapManagerFixture(t, 2, 1)
	result, err := fixture.manager.EnsureCompatible(context.Background(), 1)
	if err == nil || !result.Required || !strings.Contains(err.Error(), "node protocol") {
		t.Fatalf("compatibility result=%#v err=%v", result, err)
	}
	content, readErr := os.ReadFile(fixture.activation)
	if readErr != nil || string(content) != "old" {
		t.Fatalf("incompatible activation changed: %q %v", content, readErr)
	}
	statusData, readErr := os.ReadFile(filepath.Join(fixture.installRoot, "update-status.json"))
	var status Status
	if readErr != nil || json.Unmarshal(statusData, &status) != nil ||
		status.Phase != PhaseFailed || !strings.Contains(status.Error, "node protocol") {
		t.Fatalf("persisted compatibility failure=%#v readErr=%v", status, readErr)
	}
}

func newBootstrapManagerFixture(
	t *testing.T,
	minimumProtocol int,
	artifactProtocol int,
) bootstrapManagerFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	artifact := releaseFixtureWithProtocol(t, "v2", artifactProtocol)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	requests := &atomic.Int64{}
	var manifestBody []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path == "/artifact" {
			requests.Add(1)
			_, _ = writer.Write(artifact)
			return
		}
		_, _ = writer.Write(manifestBody)
	}))
	t.Cleanup(server.Close)
	digest := sha256.Sum256(artifact)
	manifest, err := SignManifest(Manifest{
		Version: "v2", PublishedAt: time.Now(), MinimumNodeProtocol: minimumProtocol,
		Artifacts: []Artifact{{
			OS: runtime.GOOS, Arch: runtime.GOARCH, URL: server.URL + "/artifact",
			SHA256: hex.EncodeToString(digest[:]), Size: int64(len(artifact)),
		}},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	manifestBody, _ = json.Marshal(manifest)
	root := t.TempDir()
	activation := filepath.Join(root, "bria")
	if err := os.WriteFile(activation, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	watchdogs := &atomic.Int64{}
	installRoot := filepath.Join(root, "software")
	manager, err := NewManager(ManagerConfig{
		NodeID: "node", InstallRoot: installRoot, ActivationPath: activation,
		Fetcher: Fetcher{URL: server.URL + "/manifest", PublicKey: publicKey, Client: server.Client()},
		Client:  server.Client(), Preflight: func(context.Context, string) error { return nil },
		Restart: func(string) {}, Watchdog: func(Request) error { watchdogs.Add(1); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return bootstrapManagerFixture{
		manager: manager, activation: activation, installRoot: installRoot,
		artifactRequests: requests, watchdogs: watchdogs,
	}
}
