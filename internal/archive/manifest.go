package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

const ManifestVersion = 1

type ArchiveID string

type IntegrityDigest struct {
	Algorithm string `json:"algorithm"`
	Hex       string `json:"hex"`
}

func SHA256Digest(content []byte) IntegrityDigest {
	sum := sha256.Sum256(content)
	return IntegrityDigest{Algorithm: "sha256", Hex: hex.EncodeToString(sum[:])}
}

func (d IntegrityDigest) Validate() error {
	if d.Algorithm != "sha256" {
		return fmt.Errorf("unsupported integrity algorithm: %q", d.Algorithm)
	}
	if d.Hex != strings.ToLower(d.Hex) {
		return fmt.Errorf("sha256 integrity digest must use lowercase hex")
	}
	decoded, err := hex.DecodeString(d.Hex)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("invalid sha256 integrity digest")
	}
	return nil
}

type ArtifactMetadata struct {
	Format    string          `json:"format"`
	MediaType string          `json:"media_type"`
	SizeBytes int64           `json:"size_bytes"`
	Integrity IntegrityDigest `json:"integrity"`
}

func (a ArtifactMetadata) Validate() error {
	if strings.TrimSpace(a.Format) == "" {
		return fmt.Errorf("artifact format is required")
	}
	if strings.TrimSpace(a.MediaType) == "" {
		return fmt.Errorf("artifact media type is required")
	}
	if a.SizeBytes < 0 {
		return fmt.Errorf("artifact size cannot be negative")
	}
	return a.Integrity.Validate()
}

type Manifest struct {
	Version    int                  `json:"version"`
	ID         ArchiveID            `json:"id"`
	Session    domain.SessionRef    `json:"session"`
	OwnerID    domain.UserID        `json:"owner_id"`
	Backend    string               `json:"backend"`
	CreatedAt  time.Time            `json:"created_at"`
	ArchivedAt time.Time            `json:"archived_at"`
	Reason     domain.ArchiveReason `json:"reason"`
	Artifact   ArtifactMetadata     `json:"artifact"`
}

func (m Manifest) Validate() error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("unsupported archive manifest version: %d", m.Version)
	}
	if err := validateArchiveID(m.ID); err != nil {
		return err
	}
	if err := m.Session.Validate(); err != nil {
		return err
	}
	if m.OwnerID <= 0 {
		return fmt.Errorf("archive owner is required")
	}
	if strings.TrimSpace(m.Backend) == "" {
		return fmt.Errorf("archive backend is required")
	}
	if m.CreatedAt.IsZero() || m.ArchivedAt.IsZero() {
		return fmt.Errorf("archive timestamps are required")
	}
	if m.ArchivedAt.Before(m.CreatedAt) {
		return fmt.Errorf("archive timestamp precedes session creation")
	}
	switch m.Reason {
	case domain.ArchiveManual, domain.ArchiveIdle, domain.ArchiveNodeReboot,
		domain.ArchiveResumeFailed:
	default:
		return fmt.Errorf("unsupported archive reason: %q", m.Reason)
	}
	return m.Artifact.Validate()
}

func validateArchiveID(id ArchiveID) error {
	value := string(id)
	if strings.TrimSpace(value) == "" || value == "." || value == ".." {
		return fmt.Errorf("archive id is required")
	}
	if len(value) > 128 || strings.ContainsAny(value, "/\\") {
		return fmt.Errorf("archive id is invalid")
	}
	for _, character := range value {
		if character <= 0x20 {
			return fmt.Errorf("archive id is invalid")
		}
	}
	return nil
}
