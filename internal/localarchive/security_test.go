package localarchive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/archive"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestBuildBundleRejectsInboxSymlink(t *testing.T) {
	workdir := t.TempDir()
	inbox := filepath.Join(workdir, inboxDirectory)
	if err := os.Mkdir(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(workdir, "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(inbox, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := buildBundle(context.Background(), Artifact{Version: artifactVersionV2, Workdir: workdir}); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestBuildBundlePerFileBoundary(t *testing.T) {
	for _, test := range []struct {
		name    string
		size    int64
		wantErr bool
	}{
		{name: "twenty_mib_accepted", size: maxInboxFileBytes},
		{name: "one_byte_over_rejected", size: maxInboxFileBytes + 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workdir := t.TempDir()
			inbox := filepath.Join(workdir, inboxDirectory)
			if err := os.Mkdir(inbox, 0o700); err != nil {
				t.Fatal(err)
			}
			file, err := os.Create(filepath.Join(inbox, "document"))
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(test.size); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			content, err := buildBundle(context.Background(), Artifact{
				Version: artifactVersionV2, Workdir: workdir,
			})
			if test.wantErr && (err == nil || !strings.Contains(err.Error(), "limit")) {
				t.Fatalf("oversized inbox error = %v", err)
			}
			if !test.wantErr && (err != nil || len(content) > maxBundleBytes) {
				t.Fatalf("boundary document bundle size=%d err=%v", len(content), err)
			}
		})
	}
}

func TestBuildBundleUncompressedPayloadBoundary(t *testing.T) {
	workdir := t.TempDir()
	inbox := filepath.Join(workdir, inboxDirectory)
	if err := os.Mkdir(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Version: artifactVersionV2, Workdir: workdir}
	metadata, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	secondSize := int64(maxBundlePayload-len(metadata)) - maxInboxFileBytes
	createSparseFile(t, filepath.Join(inbox, "first"), maxInboxFileBytes)
	createSparseFile(t, filepath.Join(inbox, "second"), secondSize)
	content, err := buildBundle(context.Background(), artifact)
	if err != nil || len(content) > maxBundleBytes {
		t.Fatalf("30 MiB payload bundle size=%d err=%v", len(content), err)
	}
	createSparseFile(t, filepath.Join(inbox, "over"), 1)
	if _, err := buildBundle(context.Background(), artifact); err == nil ||
		!strings.Contains(err.Error(), "30 MiB") {
		t.Fatalf("payload over boundary error = %v", err)
	}
}

func createSparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsTraversalAndSpecialEntries(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		mode os.FileMode
	}{
		{name: "traversal", path: "inbox/../../escape", mode: 0o600},
		{name: "symlink", path: "inbox/link", mode: os.ModeSymlink | 0o777},
	} {
		t.Run(test.name, func(t *testing.T) {
			workdir := t.TempDir()
			session := archiveSession(workdir, test.name)
			content := maliciousBundle(t, session, test.path, test.mode)
			writer := committedWriter(t, session, content)
			if err := writer.Verify(context.Background(), session); err == nil {
				t.Fatal("unsafe bundle was accepted")
			}
			if _, err := os.Stat(filepath.Join(workdir, "escape")); !os.IsNotExist(err) {
				t.Fatal("unsafe entry escaped workdir")
			}
		})
	}
}

func TestVerifyDoesNotOverwriteExistingInboxFile(t *testing.T) {
	workdir := t.TempDir()
	session := archiveSession(workdir, "conflict")
	content := maliciousBundle(t, session, "inbox/result.txt", 0o600)
	writer := committedWriter(t, session, content)
	inbox := filepath.Join(workdir, inboxDirectory)
	if err := os.Mkdir(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(inbox, "result.txt")
	if err := os.WriteFile(target, []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writer.Verify(context.Background(), session); err == nil {
		t.Fatal("conflicting local file was overwritten")
	}
	if got, _ := os.ReadFile(target); string(got) != "local" {
		t.Fatalf("existing content changed to %q", got)
	}
}

func TestVerifyRejectsSymlinkedDestinationParent(t *testing.T) {
	workdir := t.TempDir()
	session := archiveSession(workdir, "parent-link")
	content := maliciousBundle(t, session, "inbox/nested/result.txt", 0o600)
	writer := committedWriter(t, session, content)
	inbox := filepath.Join(workdir, inboxDirectory)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(inbox, "nested")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writer.Verify(context.Background(), session); err == nil {
		t.Fatal("symlinked destination parent was accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "result.txt")); !os.IsNotExist(err) {
		t.Fatal("restored file escaped through destination symlink")
	}
}

func archiveSession(workdir, id string) domain.Session {
	return domain.Session{ID: "session", NodeID: "node", OwnerID: 1, Backend: "codex",
		Workdir: workdir, ProviderSessionID: "provider", ArchiveID: id}
}

func maliciousBundle(t *testing.T, session domain.Session, entry string, mode os.FileMode) []byte {
	t.Helper()
	var output bytes.Buffer
	container := zip.NewWriter(&output)
	metadata, _ := json.Marshal(Artifact{Version: artifactVersionV2, Workdir: session.Workdir,
		Backend: session.Backend, ProviderSessionID: session.ProviderSessionID})
	if err := writeZipEntry(container, "session.json", metadata); err != nil {
		t.Fatal(err)
	}
	header := &zip.FileHeader{Name: entry, Method: zip.Store}
	header.SetMode(mode)
	writable, err := container.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.Write([]byte("archive")); err != nil {
		t.Fatal(err)
	}
	if err := container.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func committedWriter(t *testing.T, session domain.Session, content []byte) *Writer {
	t.Helper()
	store, err := archive.NewFileStore(filepath.Join(t.TempDir(), "archives"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(session, session.ArchiveID, artifactFormatV2, artifactMediaV2, content)
	if err := store.Commit(context.Background(), manifest, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	writer, err := NewWriter(store, transcriptStub{})
	if err != nil {
		t.Fatal(err)
	}
	return writer
}
