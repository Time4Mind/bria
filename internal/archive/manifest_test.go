package archive_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/archive"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestManifestValidatesNativeIdentityAndIntegrity(t *testing.T) {
	createdAt := time.Unix(100, 0).UTC()
	manifest := archive.Manifest{
		Version:    archive.ManifestVersion,
		ID:         "archive-1",
		Session:    domain.SessionRef{NodeID: "node-a", SessionID: "session-1"},
		OwnerID:    42,
		Backend:    "claude",
		CreatedAt:  createdAt,
		ArchivedAt: createdAt.Add(time.Hour),
		Reason:     domain.ArchiveManual,
		Artifact: archive.ArtifactMetadata{
			Format:    "bria-session-v1",
			MediaType: "application/x-bria-session+tar",
			SizeBytes: 7,
			Integrity: archive.SHA256Digest([]byte("content")),
		},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	manifest.Artifact.Integrity.Hex = strings.ToUpper(manifest.Artifact.Integrity.Hex)
	if err := manifest.Validate(); err == nil {
		t.Fatal("non-canonical digest unexpectedly accepted")
	}
}
