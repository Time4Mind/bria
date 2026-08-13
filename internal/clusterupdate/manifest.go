// Package clusterupdate verifies releases and coordinates one-node-at-a-time updates.
package clusterupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const maxManifestBytes = 1 << 20

type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	Version     string     `json:"version"`
	PublishedAt time.Time  `json:"published_at"`
	Artifacts   []Artifact `json:"artifacts"`
	Signature   string     `json:"signature"`
}

type VerifiedManifest struct {
	Manifest
	SHA256 string
}

type Fetcher struct {
	URL       string
	PublicKey ed25519.PublicKey
	Client    *http.Client
}

func DecodePublicKey(value string) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("update public key must be base64 Ed25519 public key")
	}
	return ed25519.PublicKey(decoded), nil
}

func (f Fetcher) Fetch(ctx context.Context) (VerifiedManifest, error) {
	parsed, err := url.Parse(f.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return VerifiedManifest{}, errors.New("update manifest URL must be HTTPS")
	}
	if len(f.PublicKey) != ed25519.PublicKeySize {
		return VerifiedManifest{}, errors.New("update public key is unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return VerifiedManifest{}, err
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return VerifiedManifest{}, fmt.Errorf("fetch update manifest: %w", err)
	}
	defer response.Body.Close()
	if response.Request.URL.Scheme != "https" {
		return VerifiedManifest{}, errors.New("update manifest redirected outside HTTPS")
	}
	if response.StatusCode != http.StatusOK {
		return VerifiedManifest{}, fmt.Errorf("fetch update manifest: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxManifestBytes+1))
	if err != nil || len(body) > maxManifestBytes {
		return VerifiedManifest{}, errors.New("update manifest is unreadable or too large")
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return VerifiedManifest{}, errors.New("update manifest is malformed")
	}
	if err := VerifyManifest(manifest, f.PublicKey); err != nil {
		return VerifiedManifest{}, err
	}
	digest := sha256.Sum256(body)
	return VerifiedManifest{Manifest: manifest, SHA256: hex.EncodeToString(digest[:])}, nil
}

func VerifyManifest(manifest Manifest, key ed25519.PublicKey) error {
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("update manifest signature is malformed")
	}
	payload, err := manifestSigningPayload(manifest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, payload, signature) {
		return errors.New("update manifest signature is invalid")
	}
	return nil
}

func SignManifest(manifest Manifest, key ed25519.PrivateKey) (Manifest, error) {
	if len(key) != ed25519.PrivateKeySize {
		return Manifest{}, errors.New("update private key is invalid")
	}
	payload, err := manifestSigningPayload(manifest)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))
	return manifest, nil
}

func manifestSigningPayload(manifest Manifest) ([]byte, error) {
	manifest.Version = strings.TrimSpace(manifest.Version)
	if manifest.Version == "" || manifest.PublishedAt.IsZero() || len(manifest.Artifacts) == 0 {
		return nil, errors.New("update manifest metadata is incomplete")
	}
	manifest.Signature = ""
	sort.Slice(manifest.Artifacts, func(i, j int) bool {
		left, right := manifest.Artifacts[i], manifest.Artifacts[j]
		return left.OS+"/"+left.Arch < right.OS+"/"+right.Arch
	})
	seen := make(map[string]bool, len(manifest.Artifacts))
	for index := range manifest.Artifacts {
		artifact := &manifest.Artifacts[index]
		artifact.OS, artifact.Arch = strings.TrimSpace(artifact.OS), strings.TrimSpace(artifact.Arch)
		artifact.SHA256 = strings.ToLower(strings.TrimSpace(artifact.SHA256))
		key := artifact.OS + "/" + artifact.Arch
		parsed, err := url.Parse(artifact.URL)
		if seen[key] || artifact.OS == "" || artifact.Arch == "" || len(artifact.SHA256) != 64 ||
			artifact.Size < 1 || artifact.Size > 512<<20 || err != nil || parsed.Scheme != "https" ||
			parsed.Host == "" || parsed.User != nil {
			return nil, fmt.Errorf("invalid update artifact %q", key)
		}
		if _, err := hex.DecodeString(artifact.SHA256); err != nil {
			return nil, fmt.Errorf("invalid update artifact digest %q", key)
		}
		seen[key] = true
	}
	unsigned := struct {
		Version     string     `json:"version"`
		PublishedAt time.Time  `json:"published_at"`
		Artifacts   []Artifact `json:"artifacts"`
	}{manifest.Version, manifest.PublishedAt.UTC(), manifest.Artifacts}
	return json.Marshal(unsigned)
}

func (m Manifest) Artifact(osName, arch string) (Artifact, bool) {
	for _, artifact := range m.Artifacts {
		if artifact.OS == osName && artifact.Arch == arch {
			return artifact, true
		}
	}
	return Artifact{}, false
}

// CompatibleArtifact resolves release artifacts for the platform identity stored
// in cluster state. Android nodes run Bria in a Linux userspace, so they consume
// the regular Linux ARM64 release rather than requiring a separate binary.
func (m Manifest) CompatibleArtifact(osName, arch string) (Artifact, bool) {
	osName = strings.ToLower(strings.TrimSpace(osName))
	arch = strings.ToLower(strings.TrimSpace(arch))
	if osName == "android" {
		osName = "linux"
	}
	return m.Artifact(osName, arch)
}
