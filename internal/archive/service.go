package archive

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type Request struct {
	ID         ArchiveID
	Session    domain.Session
	ArchivedAt time.Time
	Reason     domain.ArchiveReason
}

type Service struct {
	runtime RuntimeArchivePort
	writer  Writer
}

func NewService(runtime RuntimeArchivePort, writer Writer) (*Service, error) {
	if runtime == nil {
		return nil, fmt.Errorf("archive runtime is required")
	}
	if writer == nil {
		return nil, fmt.Errorf("archive writer is required")
	}
	return &Service{runtime: runtime, writer: writer}, nil
}

func (s *Service) Archive(ctx context.Context, request Request) (Manifest, error) {
	if err := validateRequest(request); err != nil {
		return Manifest{}, err
	}
	prepared, err := s.runtime.ExportArchive(ctx, request.Session.Ref())
	if err != nil {
		return Manifest{}, fmt.Errorf("export native archive: %w", err)
	}
	if prepared.Content == nil {
		return Manifest{}, fmt.Errorf("export native archive: content is required")
	}
	defer prepared.Content.Close()

	manifest := Manifest{
		Version:    ManifestVersion,
		ID:         request.ID,
		Session:    request.Session.Ref(),
		OwnerID:    request.Session.OwnerID,
		Backend:    request.Session.Backend,
		CreatedAt:  request.Session.CreatedAt.UTC(),
		ArchivedAt: request.ArchivedAt.UTC(),
		Reason:     request.Reason,
		Artifact:   prepared.Metadata,
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("invalid native archive: %w", err)
	}
	if err := s.writer.Commit(ctx, manifest, prepared.Content); err != nil {
		return Manifest{}, fmt.Errorf("commit native archive: %w", err)
	}
	if err := s.runtime.DeactivateArchived(ctx, manifest.Session, manifest.ID); err != nil {
		return manifest, &FinalizeError{Manifest: manifest, Err: err}
	}
	return manifest, nil
}

func validateRequest(request Request) error {
	if err := validateArchiveID(request.ID); err != nil {
		return err
	}
	if err := request.Session.Ref().Validate(); err != nil {
		return err
	}
	if request.Session.OwnerID <= 0 {
		return fmt.Errorf("session owner is required")
	}
	if !request.Session.IsLive() {
		return fmt.Errorf("%w: only a live session can be archived", domain.ErrInvalidState)
	}
	if request.ArchivedAt.IsZero() {
		return fmt.Errorf("archive timestamp is required")
	}
	if request.Session.CreatedAt.IsZero() {
		return fmt.Errorf("session creation timestamp is required")
	}
	if request.ArchivedAt.Before(request.Session.CreatedAt) {
		return fmt.Errorf("archive timestamp precedes session creation")
	}
	if strings.TrimSpace(request.Session.Backend) == "" {
		return fmt.Errorf("session backend is required")
	}
	if request.Reason != domain.ArchiveManual && request.Reason != domain.ArchiveIdle &&
		request.Reason != domain.ArchiveNodeReboot && request.Reason != domain.ArchiveResumeFailed {
		return fmt.Errorf("unsupported archive reason: %q", request.Reason)
	}
	return nil
}

type FinalizeError struct {
	Manifest Manifest
	Err      error
}

func (e *FinalizeError) Error() string {
	return fmt.Sprintf("archive committed but runtime deactivation failed: %v", e.Err)
}

func (e *FinalizeError) Unwrap() error {
	return e.Err
}
