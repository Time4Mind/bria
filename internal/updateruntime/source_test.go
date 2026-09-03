package updateruntime_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"bria/internal/update"
	"bria/internal/updateruntime"
)

func TestFileReleaseSourceReadsOnlyBoundedSiblingArtifact(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "release-manifest.json")
	artifactPath := filepath.Join(directory, "bria-linux-amd64.tar.gz")
	mustWriteFile(t, manifestPath, "signed-manifest")
	mustWriteFile(t, artifactPath, "artifact-bytes")
	manifestURL := (&url.URL{Scheme: "file", Path: manifestPath}).String()
	source, err := updateruntime.NewReleaseSource(updateruntime.SourceConfig{
		ManifestURL: manifestURL, TemporaryDirectory: t.TempDir(),
		MaxManifestBytes: 64, MaxArtifactBytes: 64,
	})
	if err != nil {
		t.Fatalf("NewReleaseSource: %v", err)
	}

	manifest, err := source.SignedManifest(context.Background())
	if err != nil || string(manifest) != "signed-manifest" {
		t.Fatalf("SignedManifest = %q, %v", manifest, err)
	}
	payload, err := source.Artifact(context.Background(), update.Artifact{
		Name: filepath.Base(artifactPath), Platform: "linux", Arch: "amd64", Size: int64(len("artifact-bytes")),
		SHA256: strings.Repeat("0", 64),
	})
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	defer payload.Close()
	content, err := io.ReadAll(payload)
	if err != nil || string(content) != "artifact-bytes" {
		t.Fatalf("artifact content = %q, %v", content, err)
	}
}

func TestHTTPSReleaseSourceRejectsRedirectDowngradeBeforeHTTPDestination(t *testing.T) {
	t.Parallel()
	var destinationCalls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme == "http" {
			destinationCalls.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("downgraded")), Request: request}, nil
		}
		header := make(http.Header)
		header.Set("Location", "http://updates.example/release-manifest.json")
		return &http.Response{StatusCode: http.StatusFound, Header: header, Body: io.NopCloser(strings.NewReader("redirect")), Request: request}, nil
	})}
	temporaryDirectory := t.TempDir()
	if err := os.Chmod(temporaryDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := updateruntime.NewReleaseSource(updateruntime.SourceConfig{
		ManifestURL: "https://updates.example/release-manifest.json", Client: client,
		TemporaryDirectory: temporaryDirectory, MaxManifestBytes: 64, MaxArtifactBytes: 64,
	})
	if err != nil {
		t.Fatalf("NewReleaseSource: %v", err)
	}

	if _, err := source.SignedManifest(context.Background()); !errors.Is(err, updateruntime.ErrRedirectDowngrade) {
		t.Fatalf("SignedManifest error = %v, want ErrRedirectDowngrade", err)
	}
	if destinationCalls.Load() != 0 {
		t.Fatalf("downgrade destination calls = %d, want 0", destinationCalls.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
