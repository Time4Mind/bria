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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
		Client:  server.Client(), Restart: func(path string) { restarted <- path },
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

func releaseFixture(t *testing.T, version string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	content := []byte("#!/bin/sh\nprintf '{\"version\":\"" + version + "\"}\\n'\n")
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
