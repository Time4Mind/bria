package archive_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/archive"
	"github.com/Time4Mind/bria/internal/domain"
)

type fakeRuntime struct {
	content       []byte
	metadata      archive.ArtifactMetadata
	exportErr     error
	deactivateErr error
	operations    *[]string
}

func (r *fakeRuntime) ExportArchive(
	_ context.Context,
	_ domain.SessionRef,
) (archive.PreparedArtifact, error) {
	*r.operations = append(*r.operations, "export")
	if r.exportErr != nil {
		return archive.PreparedArtifact{}, r.exportErr
	}
	return archive.PreparedArtifact{
		Metadata: r.metadata,
		Content:  io.NopCloser(bytes.NewReader(r.content)),
	}, nil
}

func (r *fakeRuntime) DeactivateArchived(
	_ context.Context,
	_ domain.SessionRef,
	_ archive.ArchiveID,
) error {
	*r.operations = append(*r.operations, "deactivate")
	return r.deactivateErr
}

type fakeWriter struct {
	err        error
	operations *[]string
	manifest   archive.Manifest
	content    []byte
}

func (w *fakeWriter) Commit(
	_ context.Context,
	manifest archive.Manifest,
	content io.Reader,
) error {
	*w.operations = append(*w.operations, "commit")
	if w.err != nil {
		return w.err
	}
	w.manifest = manifest
	w.content, _ = io.ReadAll(content)
	return nil
}

func archiveFixture(t *testing.T) (*archive.Service, archive.Request, *fakeRuntime, *fakeWriter, *[]string) {
	t.Helper()
	content := []byte("native bria archive\n")
	operations := make([]string, 0, 3)
	runtime := &fakeRuntime{
		content: content,
		metadata: archive.ArtifactMetadata{
			Format:    "bria-session-v1",
			MediaType: "application/x-bria-session+tar",
			SizeBytes: int64(len(content)),
			Integrity: archive.SHA256Digest(content),
		},
		operations: &operations,
	}
	writer := &fakeWriter{operations: &operations}
	service, err := archive.NewService(runtime, writer)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	createdAt := time.Unix(100, 0)
	request := archive.Request{
		ID: "archive-1",
		Session: domain.Session{
			ID:                "session-1",
			NodeID:            "node-a",
			OwnerID:           42,
			Name:              "Build",
			Backend:           "claude",
			ProviderSessionID: "provider-must-not-be-the-archive",
			State:             domain.SessionActive,
			CreatedAt:         createdAt,
			LiveSinceAt:       createdAt,
		},
		ArchivedAt: time.Unix(200, 0),
		Reason:     domain.ArchiveManual,
	}
	return service, request, runtime, writer, &operations
}

func TestServiceCommitsNativeArtifactBeforeRuntimeDeactivation(t *testing.T) {
	service, request, _, writer, operations := archiveFixture(t)

	manifest, err := service.Archive(context.Background(), request)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if got, want := *operations, []string{"export", "commit", "deactivate"}; !equalStrings(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	if manifest.Session != (domain.SessionRef{NodeID: "node-a", SessionID: "session-1"}) {
		t.Fatalf("manifest session = %#v", manifest.Session)
	}
	if manifest.OwnerID != 42 || manifest.Backend != "claude" {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	if !bytes.Equal(writer.content, []byte("native bria archive\n")) {
		t.Fatalf("committed content = %q", writer.content)
	}
	if writer.manifest.Artifact.Integrity != archive.SHA256Digest(writer.content) {
		t.Fatal("manifest digest does not identify committed native content")
	}
}

func TestCommitFailureLeavesRuntimeLive(t *testing.T) {
	service, request, _, writer, operations := archiveFixture(t)
	writer.err = errors.New("storage full")

	_, err := service.Archive(context.Background(), request)
	if err == nil {
		t.Fatal("commit failure unexpectedly succeeded")
	}
	if got, want := *operations, []string{"export", "commit"}; !equalStrings(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

func TestDeactivateFailureReportsAlreadyCommittedManifest(t *testing.T) {
	service, request, runtime, _, operations := archiveFixture(t)
	runtime.deactivateErr = errors.New("tmux unavailable")

	manifest, err := service.Archive(context.Background(), request)
	var finalize *archive.FinalizeError
	if !errors.As(err, &finalize) {
		t.Fatalf("error = %v, want FinalizeError", err)
	}
	if finalize.Manifest.ID != request.ID || manifest.ID != request.ID {
		t.Fatalf("committed manifest was not returned: %#v", finalize.Manifest)
	}
	if got, want := *operations, []string{"export", "commit", "deactivate"}; !equalStrings(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

func TestServiceRejectsNonLiveSessionBeforeUsingPorts(t *testing.T) {
	service, request, _, _, operations := archiveFixture(t)
	request.Session.State = domain.SessionArchived

	_, err := service.Archive(context.Background(), request)
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("error = %v, want invalid state", err)
	}
	if len(*operations) != 0 {
		t.Fatalf("ports called for invalid request: %v", *operations)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
