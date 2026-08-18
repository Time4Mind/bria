package clusterupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManagerDownloadsVerifiesActivatesAndConfirms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	artifact := releaseFixture(t, "v2")
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var manifestBody []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/artifact" {
			_, _ = writer.Write(artifact)
			return
		}
		_, _ = writer.Write(manifestBody)
	}))
	defer server.Close()
	digest := sha256.Sum256(artifact)
	manifest, err := SignManifest(Manifest{
		Version: "v2", PublishedAt: time.Now(), Artifacts: []Artifact{{
			OS: runtime.GOOS, Arch: runtime.GOARCH, URL: server.URL + "/artifact",
			SHA256: hex.EncodeToString(digest[:]), Size: int64(len(artifact)),
		}},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	manifestBody, _ = json.Marshal(manifest)
	manifestDigest := sha256.Sum256(manifestBody)
	root := t.TempDir()
	activation := filepath.Join(root, "bria")
	if err := os.WriteFile(activation, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	restarted := make(chan string, 1)
	manager, err := NewManager(ManagerConfig{
		NodeID: "node", InstallRoot: filepath.Join(root, "software"), ActivationPath: activation,
		Fetcher: Fetcher{URL: server.URL + "/manifest", PublicKey: publicKey, Client: server.Client()},
		Client:  server.Client(), Preflight: func(context.Context, string) error { return nil },
		Restart:  func(path string) { restarted <- path },
		Watchdog: func(Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		NodeID: "node", UpdateID: "job", Version: "v2",
		ManifestSHA256: hex.EncodeToString(manifestDigest[:]),
	}
	if _, err := manager.Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-restarted:
		if path != activation {
			t.Fatalf("restart path = %q", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("manager status: %#v", func() Status { value, _ := manager.Status(context.Background(), request); return value }())
	}
	resolved, err := filepath.EvalSymlinks(activation)
	if err != nil || filepath.Base(resolved) != "bria" || resolved == activation {
		t.Fatalf("activation target = %q, err=%v", resolved, err)
	}
	if err := ConfirmInstalled(filepath.Join(root, "software"), "v2"); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRejectsIncompatibleCandidateBeforeActivation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	artifact := releaseFixture(t, "v2")
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var manifestBody []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/artifact" {
			_, _ = writer.Write(artifact)
			return
		}
		_, _ = writer.Write(manifestBody)
	}))
	defer server.Close()
	digest := sha256.Sum256(artifact)
	manifest, err := SignManifest(Manifest{
		Version: "v2", PublishedAt: time.Now(), Artifacts: []Artifact{{
			OS: runtime.GOOS, Arch: runtime.GOARCH, URL: server.URL + "/artifact",
			SHA256: hex.EncodeToString(digest[:]), Size: int64(len(artifact)),
		}},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	manifestBody, _ = json.Marshal(manifest)
	manifestDigest := sha256.Sum256(manifestBody)
	root := t.TempDir()
	activation := filepath.Join(root, "bria")
	if err := os.WriteFile(activation, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	restarted := make(chan string, 1)
	manager, err := NewManager(ManagerConfig{
		NodeID: "node", InstallRoot: filepath.Join(root, "software"), ActivationPath: activation,
		Fetcher: Fetcher{URL: server.URL + "/manifest", PublicKey: publicKey, Client: server.Client()},
		Client:  server.Client(),
		Preflight: func(context.Context, string) error {
			return errors.New(`decode config: json: unknown field "runner"`)
		},
		Restart:  func(path string) { restarted <- path },
		Watchdog: func(Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		NodeID: "node", UpdateID: "incompatible", Version: "v2",
		ManifestSHA256: hex.EncodeToString(manifestDigest[:]),
	}
	if _, err := manager.Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, statusErr := manager.Status(context.Background(), request)
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if status.Phase == PhaseFailed {
			if !strings.Contains(status.Error, `unknown field "runner"`) {
				t.Fatalf("status=%#v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("manager did not fail preflight: %#v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case path := <-restarted:
		t.Fatalf("incompatible candidate restarted %q", path)
	default:
	}
	content, err := os.ReadFile(activation)
	if err != nil || string(content) != "old" {
		t.Fatalf("activation changed: %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "software", "update-pending.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending update exists after rejected preflight: %v", err)
	}
}

func releaseFixture(t *testing.T, version string) []byte {
	return releaseFixtureWithProtocol(t, version, 0)
}

func releaseFixtureWithProtocol(t *testing.T, version string, protocol int) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	content := []byte(fmt.Sprintf(
		"#!/bin/sh\nprintf '{\"version\":\"%s\",\"node_protocol\":%d}\\n'\n",
		version, protocol,
	))
	if err := tarWriter.WriteHeader(&tar.Header{Name: "bria", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
