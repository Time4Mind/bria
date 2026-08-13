package archive

import (
	"context"
	"io"

	"github.com/Time4Mind/bria/internal/domain"
)

type PreparedArtifact struct {
	Metadata ArtifactMetadata
	Content  io.ReadCloser
}

// RuntimeArchivePort exports native Bria content without stopping the
// session, then deactivates it only after the archive writer has committed.
type RuntimeArchivePort interface {
	ExportArchive(context.Context, domain.SessionRef) (PreparedArtifact, error)
	DeactivateArchived(context.Context, domain.SessionRef, ArchiveID) error
}

// Writer atomically publishes a manifest and its artifact. Commit must verify
// content size and digest against manifest.Artifact before making either item
// visible. Repeating an identical archive ID is idempotent; different content
// for an existing ID is an error.
type Writer interface {
	Commit(context.Context, Manifest, io.Reader) error
}
