package clusterupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCompatibilityFloorManifestKeepsLegacySigningShape(t *testing.T) {
	manifest := Manifest{
		Version: "v1", PublishedAt: time.Unix(123, 0).UTC(),
		Artifacts: []Artifact{{
			OS: "linux", Arch: "amd64", URL: "https://example.test/bria.tar.gz",
			SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 42,
		}},
	}
	payload, err := manifestSigningPayload(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("minimum_node_protocol")) {
		t.Fatalf("floor-zero manifest broke the legacy signing shape: %s", payload)
	}
}

func TestFetcherVerifiesSignedManifestAndPinsRawDigest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := SignManifest(Manifest{
		Version: "v1.2.3", PublishedAt: time.Unix(123, 0), MinimumNodeProtocol: 2,
		Artifacts: []Artifact{{
			OS: "linux", Arch: "amd64", URL: "https://example.test/bria.tar.gz",
			SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 42,
		}},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(manifest)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	verified, err := (Fetcher{URL: server.URL, PublicKey: publicKey, Client: server.Client()}).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if verified.Version != manifest.Version || verified.MinimumNodeProtocol != 2 ||
		len(verified.SHA256) != 64 {
		t.Fatalf("unexpected verified manifest: %#v", verified)
	}
	manifest.MinimumNodeProtocol = 3
	if err := VerifyManifest(manifest, publicKey); err == nil {
		t.Fatal("tampered manifest signature was accepted")
	}
}

func TestManifestRejectsDuplicatePlatformAndInsecureArtifact(t *testing.T) {
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	_, err := SignManifest(Manifest{
		Version: "v1", PublishedAt: time.Now(), Artifacts: []Artifact{
			{OS: "linux", Arch: "arm64", URL: "http://example.test/a", SHA256: string(make([]byte, 64)), Size: 1},
			{OS: "linux", Arch: "arm64", URL: "https://example.test/b", SHA256: string(make([]byte, 64)), Size: 1},
		},
	}, privateKey)
	if err == nil {
		t.Fatal("unsafe manifest was accepted")
	}
}

func TestCompatibleArtifactUsesLinuxARM64ForAndroid(t *testing.T) {
	manifest := Manifest{Artifacts: []Artifact{{OS: "linux", Arch: "arm64", URL: "linux"}}}
	artifact, ok := manifest.CompatibleArtifact(" Android ", "ARM64")
	if !ok || artifact.URL != "linux" {
		t.Fatalf("Android artifact = %#v, %t", artifact, ok)
	}
}
