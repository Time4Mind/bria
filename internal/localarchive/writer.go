package localarchive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Time4Mind/bria/internal/archive"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/transcript"
)

const (
	artifactVersionV1 = 1
	artifactVersionV2 = 2
	artifactFormatV1  = "bria-session-v1"
	artifactFormatV2  = "bria-session-v2"
	artifactMediaV1   = "application/vnd.bria.session+json"
	artifactMediaV2   = "application/vnd.bria.session+zip"
)

type TranscriptSource interface {
	Read(context.Context, transcript.Request) ([]transcript.Event, error)
}

type Writer struct {
	store  *archive.FileStore
	source TranscriptSource
}

type Artifact struct {
	Version            int                `json:"version"`
	Name               string             `json:"name"`
	Workdir            string             `json:"workdir"`
	Backend            string             `json:"backend"`
	ProviderSessionID  string             `json:"provider_session_id"`
	InitialUserPrompts []string           `json:"initial_user_prompts,omitempty"`
	Events             []transcript.Event `json:"events"`
}

func NewWriter(store *archive.FileStore, source TranscriptSource) (*Writer, error) {
	if store == nil || source == nil {
		return nil, errors.New("archive store and transcript source are required")
	}
	return &Writer{store: store, source: source}, nil
}

func (w *Writer) Commit(ctx context.Context, request runtimehost.Request) error {
	payload := request.Archive
	if payload == nil || payload.ArchiveID != request.ArchiveCommitID {
		return errors.New("archive payload is invalid")
	}
	var events []transcript.Event
	if payload.ProviderSessionID != "" {
		transcriptRequest := transcript.Request{
			Backend: transcript.Backend(request.Backend), ProviderSessionID: payload.ProviderSessionID,
			Workdir: payload.Workdir,
		}
		var err error
		events, err = w.source.Read(ctx, transcriptRequest)
		if err != nil && !errors.Is(err, transcript.ErrTranscriptNotFound) {
			return fmt.Errorf("read archive transcript: %w", err)
		}
	}
	initialPrompts := firstArchivedUserPrompts(events, 3)
	artifact := Artifact{
		Version: artifactVersionV2, Name: payload.Name, Workdir: payload.Workdir,
		Backend: request.Backend, ProviderSessionID: payload.ProviderSessionID,
		InitialUserPrompts: initialPrompts, Events: events,
	}
	reason := domain.ArchiveReason(payload.Reason)
	if reason == "" {
		reason = domain.ArchiveManual
	}
	content, err := buildBundle(ctx, artifact)
	if err != nil {
		return fmt.Errorf("encode native archive bundle: %w", err)
	}
	manifest := archive.Manifest{
		Version: archive.ManifestVersion, ID: archive.ArchiveID(payload.ArchiveID),
		Session: domain.SessionRef{
			NodeID: domain.NodeID(request.NodeID), SessionID: domain.SessionID(request.SessionID),
		},
		OwnerID: domain.UserID(payload.OwnerID), Backend: request.Backend,
		CreatedAt: payload.CreatedAt.UTC(), ArchivedAt: payload.ArchivedAt.UTC(),
		Reason: reason,
		Artifact: archive.ArtifactMetadata{
			Format: artifactFormatV2, MediaType: artifactMediaV2,
			SizeBytes: int64(len(content)), Integrity: archive.SHA256Digest(content),
		},
	}
	return w.store.Commit(ctx, manifest, bytes.NewReader(content))
}

func firstArchivedUserPrompts(events []transcript.Event, limit int) []string {
	result := make([]string, 0, limit)
	for _, event := range events {
		if event.Kind != transcript.EventUserText || strings.TrimSpace(event.Text) == "" {
			continue
		}
		result = append(result, event.Text)
		if len(result) == limit {
			break
		}
	}
	return result
}

func (w *Writer) ReadArchivedInitialUserPrompts(
	ctx context.Context,
	session domain.Session,
) ([]string, error) {
	artifact, err := w.readArchivedArtifact(ctx, session)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), artifact.InitialUserPrompts...), nil
}

func (w *Writer) Verify(ctx context.Context, session domain.Session) error {
	manifest, content, err := w.store.Load(ctx, archive.ArchiveID(session.ArchiveID))
	if err != nil {
		return fmt.Errorf("load native archive: %w", err)
	}
	if manifest.Session != session.Ref() || manifest.OwnerID != session.OwnerID ||
		!strings.EqualFold(manifest.Backend, session.Backend) {
		return errors.New("native archive identity does not match session")
	}
	artifact, inbox, err := decodeArtifact(manifest, content)
	if err != nil {
		return err
	}
	if artifact.Workdir != session.Workdir || !strings.EqualFold(artifact.Backend, session.Backend) {
		return errors.New("native archive runtime metadata does not match session")
	}
	if artifact.ProviderSessionID != session.ProviderSessionID {
		if artifact.ProviderSessionID != "" || session.ProviderSessionID == "" ||
			len(artifact.Events) != 0 {
			return errors.New("native archive runtime metadata does not match session")
		}
		// Legacy bundles created while the lifecycle hook was unavailable have
		// neither a provider identity nor transcript events. Accept a repaired
		// identity only when the origin provider still exposes that exact thread
		// in the archived workdir.
		if _, readErr := w.source.Read(ctx, transcript.Request{
			Backend:           transcript.Backend(session.Backend),
			ProviderSessionID: session.ProviderSessionID,
			Workdir:           session.Workdir,
		}); readErr != nil {
			return fmt.Errorf("verify recovered provider transcript: %w", readErr)
		}
	}
	if len(inbox) == 0 {
		return nil
	}
	if err := restoreInbox(ctx, session.Workdir, inbox); err != nil {
		return fmt.Errorf("restore native archive inbox: %w", err)
	}
	return nil
}

func (w *Writer) ReadArchivedTranscript(
	ctx context.Context,
	session domain.Session,
) ([]transcript.Event, error) {
	artifact, err := w.readArchivedArtifact(ctx, session)
	if err != nil {
		return nil, err
	}
	return append([]transcript.Event(nil), artifact.Events...), nil
}

func (w *Writer) readArchivedArtifact(
	ctx context.Context,
	session domain.Session,
) (Artifact, error) {
	if session.ArchiveID == "" {
		return Artifact{}, transcript.ErrTranscriptNotFound
	}
	manifest, content, err := w.store.Load(ctx, archive.ArchiveID(session.ArchiveID))
	if err != nil {
		return Artifact{}, fmt.Errorf("load native archive transcript: %w", err)
	}
	if manifest.Session != session.Ref() || manifest.OwnerID != session.OwnerID ||
		!strings.EqualFold(manifest.Backend, session.Backend) {
		return Artifact{}, errors.New("native archive transcript identity does not match session")
	}
	artifact, _, err := decodeArtifact(manifest, content)
	if err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func decodeArtifact(manifest archive.Manifest, content []byte) (Artifact, []inboxFile, error) {
	var artifact Artifact
	switch {
	case manifest.Artifact.Format == artifactFormatV1 && manifest.Artifact.MediaType == artifactMediaV1:
		if err := json.Unmarshal(content, &artifact); err != nil {
			return Artifact{}, nil, fmt.Errorf("decode native archive: %w", err)
		}
		if artifact.Version != artifactVersionV1 {
			return Artifact{}, nil, errors.New("native archive version does not match manifest format")
		}
		return artifact, nil, nil
	case manifest.Artifact.Format == artifactFormatV2 && manifest.Artifact.MediaType == artifactMediaV2:
		return readBundle(content)
	default:
		return Artifact{}, nil, errors.New("unsupported native archive format")
	}
}

func (w *Writer) Finalize(_ context.Context, request runtimehost.Request) error {
	if request.Archive == nil || request.Archive.ArchiveID != request.ArchiveCommitID {
		return errors.New("archive payload is invalid")
	}
	return w.FinalizeArchive(request.ArchiveCommitID)
}

func (w *Writer) FinalizeArchive(archiveID string) error {
	return w.store.MarkReady(archive.ArchiveID(archiveID))
}

func (w *Writer) ReadyArchiveIDs() ([]string, error) {
	ids, err := w.store.ReadyIDs()
	if err != nil {
		return nil, err
	}
	result := make([]string, len(ids))
	for index, id := range ids {
		result[index] = string(id)
	}
	return result, nil
}

// DeleteArchive is the origin-node side of a replicated SessionTombstone.
// It only removes the Bria archive bundle; workdir, .bria-inbox, and provider
// transcript storage remain outside this lifecycle.
func (w *Writer) DeleteArchive(ctx context.Context, archiveID string) error {
	return w.store.Delete(ctx, archive.ArchiveID(archiveID))
}
