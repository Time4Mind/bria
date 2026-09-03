// Package update verifies owner-signed release metadata and drives a durable,
// sequential rollout state machine. Downloading, installation, and persistence
// are intentionally supplied by composition code outside this package.
package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

const ManifestFormatVersion = 1

var (
	ErrInvalidManifest  = errors.New("invalid release manifest")
	ErrInvalidSignature = errors.New("invalid release signature")
	ErrUntrustedKey     = errors.New("untrusted release signing key")
	ErrNoArtifact       = errors.New("no release artifact for platform")
	ErrArtifactHash     = errors.New("release artifact checksum mismatch")
	ErrInvalidRollout   = errors.New("invalid rollout")
	ErrNodeUnavailable  = errors.New("rollout node is not online")
	ErrUnexpectedState  = errors.New("unexpected rollout state")
	ErrUnhealthyReceipt = errors.New("unhealthy rollout receipt")
	ErrRollbackFailed   = errors.New("release rollback failed")
)

type Artifact struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

type Manifest struct {
	FormatVersion int        `json:"format_version"`
	Version       string     `json:"version"`
	Artifacts     []Artifact `json:"artifacts"`
}

// ArtifactSource is one versioned release artifact presented by release
// tooling. BuildManifest consumes Content exactly once and does not own it.
type ArtifactSource struct {
	Name     string
	Platform string
	Arch     string
	Content  io.Reader
}

// BuildManifest derives sizes and checksums from the actual artifact bytes and
// returns canonical ordering suitable for signing.
func BuildManifest(version string, sources []ArtifactSource) (Manifest, error) {
	if !validLabel(version, 128) || len(sources) == 0 {
		return Manifest{}, fmt.Errorf("%w: release version and artifact sources are required", ErrInvalidManifest)
	}
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if source.Content == nil || !validArtifactIdentity(source.Name, source.Platform, source.Arch) {
			return Manifest{}, fmt.Errorf("%w: invalid artifact source", ErrInvalidManifest)
		}
		key := source.Platform + "\x00" + source.Arch
		if _, duplicate := seen[key]; duplicate {
			return Manifest{}, fmt.Errorf("%w: duplicate artifact for %s/%s", ErrInvalidManifest, source.Platform, source.Arch)
		}
		seen[key] = struct{}{}
	}

	manifest := Manifest{FormatVersion: ManifestFormatVersion, Version: version, Artifacts: make([]Artifact, 0, len(sources))}
	for _, source := range sources {
		hash := sha256.New()
		size, err := io.Copy(hash, source.Content)
		if err != nil {
			return Manifest{}, fmt.Errorf("read release artifact %q: %w", source.Name, err)
		}
		manifest.Artifacts = append(manifest.Artifacts, Artifact{
			Name: source.Name, Platform: source.Platform, Arch: source.Arch,
			Size: size, SHA256: hex.EncodeToString(hash.Sum(nil)),
		})
	}
	sort.Slice(manifest.Artifacts, func(i, j int) bool {
		return artifactKey(manifest.Artifacts[i]) < artifactKey(manifest.Artifacts[j])
	})
	if _, err := MarshalManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// MarshalManifest emits the canonical bytes that an owner signs. Artifacts
// must already be in canonical platform/architecture/name order.
func MarshalManifest(manifest Manifest) ([]byte, error) {
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	if !sort.SliceIsSorted(manifest.Artifacts, func(i, j int) bool {
		return artifactKey(manifest.Artifacts[i]) < artifactKey(manifest.Artifacts[j])
	}) {
		return nil, fmt.Errorf("%w: artifacts are not in canonical order", ErrInvalidManifest)
	}
	return json.Marshal(manifest)
}

// VerifyManifest checks a detached signature when the caller has already
// selected a pinned public key. Release tooling that carries a key reference
// must use VerifySignedManifest instead.
func VerifyManifest(manifestBytes, signature []byte, publicKey ed25519.PublicKey) (Manifest, error) {
	if len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, manifestBytes, signature) {
		return Manifest{}, ErrInvalidSignature
	}
	return parseCanonicalManifest(manifestBytes)
}

func parseCanonicalManifest(manifestBytes []byte) (Manifest, error) {
	decoder := json.NewDecoder(strings.NewReader(string(manifestBytes)))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Manifest{}, fmt.Errorf("%w: trailing manifest data", ErrInvalidManifest)
	}
	canonical, err := MarshalManifest(manifest)
	if err != nil {
		return Manifest{}, err
	}
	if string(canonical) != string(manifestBytes) {
		return Manifest{}, fmt.Errorf("%w: manifest bytes are not canonical", ErrInvalidManifest)
	}
	return manifest, nil
}

func SelectArtifact(manifest Manifest, platform, arch string) (Artifact, error) {
	if err := validateManifest(manifest); err != nil {
		return Artifact{}, err
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.Platform == platform && artifact.Arch == arch {
			return artifact, nil
		}
	}
	return Artifact{}, fmt.Errorf("%w: %s/%s", ErrNoArtifact, platform, arch)
}

// VerifyArtifact consumes the payload and checks both its declared size and
// SHA-256 digest before it may be staged for installation.
func VerifyArtifact(payload io.Reader, artifact Artifact) error {
	if err := validateArtifact(artifact); err != nil {
		return err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, payload)
	if err != nil {
		return fmt.Errorf("read release artifact: %w", err)
	}
	if size != artifact.Size || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return ErrArtifactHash
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	if manifest.FormatVersion != ManifestFormatVersion || !validLabel(manifest.Version, 128) || len(manifest.Artifacts) == 0 {
		return fmt.Errorf("%w: format version, release version, and artifacts are required", ErrInvalidManifest)
	}
	seen := make(map[string]struct{}, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if err := validateArtifact(artifact); err != nil {
			return err
		}
		key := artifact.Platform + "\x00" + artifact.Arch
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: duplicate artifact for %s/%s", ErrInvalidManifest, artifact.Platform, artifact.Arch)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateArtifact(artifact Artifact) error {
	if !validArtifactIdentity(artifact.Name, artifact.Platform, artifact.Arch) || artifact.Size < 0 || len(artifact.SHA256) != sha256.Size*2 {
		return fmt.Errorf("%w: invalid artifact metadata", ErrInvalidManifest)
	}
	if _, err := hex.DecodeString(artifact.SHA256); err != nil || strings.ToLower(artifact.SHA256) != artifact.SHA256 {
		return fmt.Errorf("%w: invalid artifact checksum", ErrInvalidManifest)
	}
	return nil
}

func validArtifactIdentity(name, platform, arch string) bool {
	return validLabel(name, 255) && validLabel(platform, 64) && validLabel(arch, 64)
}

func validLabel(value string, maxLength int) bool {
	if value == "" || len(value) > maxLength || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._+-", character) {
			continue
		}
		return false
	}
	return true
}

func artifactKey(artifact Artifact) string {
	return artifact.Platform + "\x00" + artifact.Arch + "\x00" + artifact.Name
}
