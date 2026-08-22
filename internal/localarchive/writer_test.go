package localarchive

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/archive"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/transcript"
)

type transcriptStub struct {
	events []transcript.Event
}

func (s transcriptStub) Read(context.Context, transcript.Request) ([]transcript.Event, error) {
	return append([]transcript.Event(nil), s.events...), nil
}

type recordingTranscriptStub struct {
	events   []transcript.Event
	requests *[]transcript.Request
}

func (s recordingTranscriptStub) Read(
	_ context.Context,
	request transcript.Request,
) ([]transcript.Event, error) {
	*s.requests = append(*s.requests, request)
	return append([]transcript.Event(nil), s.events...), nil
}

func TestWriterCommitsAndVerifiesNativeArtifact(t *testing.T) {
	store, err := archive.NewFileStore(filepath.Join(t.TempDir(), "archives"))
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewWriter(store, transcriptStub{
		events: []transcript.Event{
			{Kind: transcript.EventUserText, Text: "first"},
			{Kind: transcript.EventUserText, Text: "second"},
			{Kind: transcript.EventUserText, Text: "third"},
			{Kind: transcript.EventAssistantFinal, Text: "done"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Unix(10, 0).UTC()
	workdir := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(filepath.Join(workdir, inboxDirectory, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	attachment := filepath.Join(workdir, inboxDirectory, "nested", "report.txt")
	if err := os.WriteFile(attachment, []byte("result"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := runtimehost.Request{
		OperationID: "close", ActorID: 7, NodeID: "node", SessionID: "session",
		ExpectedGeneration: 1, Action: runtimehost.ActionClose, Backend: "claude",
		ArchiveCommitID: "archive-close",
		Archive: &runtimehost.ArchivePayload{
			ArchiveID: "archive-close", OwnerID: 7, Name: "work",
			Workdir: workdir, ProviderSessionID: "provider",
			CreatedAt: createdAt, ArchivedAt: createdAt.Add(time.Minute),
		},
	}
	if err := writer.Commit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(context.Background(), request); err != nil {
		t.Fatalf("repeat commit was not idempotent: %v", err)
	}
	if ids, err := writer.ReadyArchiveIDs(); err != nil || len(ids) != 0 {
		t.Fatalf("archive was advertised before runtime deactivation: ids=%v err=%v", ids, err)
	}
	if err := writer.Finalize(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if ids, err := writer.ReadyArchiveIDs(); err != nil ||
		len(ids) != 1 || ids[0] != "archive-close" {
		t.Fatalf("ready archive inventory=%v err=%v", ids, err)
	}
	session := domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Backend: "claude",
		Workdir: workdir, ProviderSessionID: "provider", ArchiveID: "archive-close",
	}
	if err := os.RemoveAll(filepath.Join(workdir, inboxDirectory)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Verify(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if restored, err := os.ReadFile(attachment); err != nil || string(restored) != "result" {
		t.Fatalf("restored attachment=%q err=%v", restored, err)
	}
	if err := writer.Verify(context.Background(), session); err != nil {
		t.Fatalf("repeat verify was not idempotent: %v", err)
	}
	manifest, content, err := store.Load(context.Background(), "archive-close")
	if err != nil || manifest.Artifact.Format != artifactFormatV2 ||
		manifest.Artifact.MediaType != artifactMediaV2 || len(content) == 0 {
		t.Fatalf("manifest=%#v content=%d err=%v", manifest, len(content), err)
	}
	archived := domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Backend: "claude",
		ArchiveID: "archive-close",
	}
	events, err := writer.ReadArchivedTranscript(context.Background(), archived)
	if err != nil || len(events) != 4 || events[3].Text != "done" {
		t.Fatalf("archived events=%#v err=%v", events, err)
	}
	prompts, err := writer.ReadArchivedInitialUserPrompts(context.Background(), archived)
	if err != nil || len(prompts) != 3 || prompts[0] != "first" {
		t.Fatalf("archived prompts=%#v err=%v", prompts, err)
	}
	if err := writer.DeleteArchive(context.Background(), "archive-close"); err != nil {
		t.Fatal(err)
	}
	if err := writer.DeleteArchive(context.Background(), "archive-close"); err != nil {
		t.Fatalf("repeat archive delete: %v", err)
	}
	if _, err := writer.ReadArchivedTranscript(context.Background(), archived); err == nil {
		t.Fatal("deleted archive transcript remained readable")
	}
	if restored, err := os.ReadFile(attachment); err != nil || string(restored) != "result" {
		t.Fatalf("archive delete touched workdir attachment=%q err=%v", restored, err)
	}
}

func TestWriterVerifiesLegacyV1Artifact(t *testing.T) {
	root := t.TempDir()
	store, err := archive.NewFileStore(filepath.Join(root, "archives"))
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewWriter(store, transcriptStub{})
	if err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(root, "work")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Version: artifactVersionV1, Workdir: workdir, Backend: "codex", ProviderSessionID: "p"}
	content, _ := json.Marshal(artifact)
	session := domain.Session{ID: "s", NodeID: "n", OwnerID: 1, Backend: "codex", Workdir: workdir,
		ProviderSessionID: "p", ArchiveID: "legacy"}
	manifest := testManifest(session, "legacy", artifactFormatV1, artifactMediaV1, content)
	if err := store.Commit(context.Background(), manifest, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Verify(context.Background(), session); err != nil {
		t.Fatal(err)
	}
}

func TestWriterVerifiesRecoveredProviderForEmptyLegacyArtifact(t *testing.T) {
	root := t.TempDir()
	store, err := archive.NewFileStore(filepath.Join(root, "archives"))
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]transcript.Request, 0, 1)
	writer, err := NewWriter(store, recordingTranscriptStub{
		events:   []transcript.Event{{Kind: transcript.EventUserText, Text: "recover"}},
		requests: &requests,
	})
	if err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(root, "work")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Version: artifactVersionV1, Workdir: workdir, Backend: "codex"}
	content, _ := json.Marshal(artifact)
	session := domain.Session{
		ID: "s", NodeID: "n", OwnerID: 1, Backend: "codex", Workdir: workdir,
		ProviderSessionID: "provider-recovered", ArchiveID: "legacy-recovered",
	}
	manifest := testManifest(
		session, "legacy-recovered", artifactFormatV1, artifactMediaV1, content,
	)
	if err := store.Commit(context.Background(), manifest, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Verify(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].ProviderSessionID != "provider-recovered" ||
		requests[0].Workdir != workdir {
		t.Fatalf("provider verification requests=%#v", requests)
	}
}

func testManifest(session domain.Session, id, format, media string, content []byte) archive.Manifest {
	created := time.Unix(1, 0).UTC()
	return archive.Manifest{
		Version: archive.ManifestVersion, ID: archive.ArchiveID(id), Session: session.Ref(),
		OwnerID: session.OwnerID, Backend: session.Backend, CreatedAt: created,
		ArchivedAt: created.Add(time.Second), Reason: domain.ArchiveManual,
		Artifact: archive.ArtifactMetadata{Format: format, MediaType: media,
			SizeBytes: int64(len(content)), Integrity: archive.SHA256Digest(content)},
	}
}
