package updateruntime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/update"
	"bria/internal/updateflow"
	"bria/internal/updateruntime"
)

func TestLocalStagerRejectsWorldReadableExistingRoot(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "unsafe-stage")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := updateruntime.OpenLocalStager(directory, 1024); !errors.Is(err, updateruntime.ErrInvalidStage) {
		t.Fatalf("OpenLocalStager error = %v, want ErrInvalidStage", err)
	}
}

func TestLocalStagerIsIdempotentAndReceiptSurvivesReopen(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "stages")
	payload := []byte("verified release archive")
	digest := sha256.Sum256(payload)
	request := updateflow.StageRequest{
		OperationID: "update:stage:stable:1", NodeID: "executor", Version: "2.0.0",
		SignedManifest: []byte(`{"signed":"manifest"}`),
		Artifact: update.Artifact{
			Name: "bria-linux-amd64.tar.gz", Platform: "linux", Arch: "amd64",
			Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]),
		},
		Content: bytes.NewReader(payload),
	}
	stager, err := updateruntime.OpenLocalStager(directory, 1024)
	if err != nil {
		t.Fatalf("OpenLocalStager: %v", err)
	}
	first, err := stager.Stage(context.Background(), request)
	if err != nil {
		t.Fatalf("Stage first: %v", err)
	}
	reopened, err := updateruntime.OpenLocalStager(directory, 1024)
	if err != nil {
		t.Fatalf("reopen stager: %v", err)
	}
	request.Content = bytes.NewReader(payload)
	second, err := reopened.Stage(context.Background(), request)
	if err != nil {
		t.Fatalf("Stage replay: %v", err)
	}
	if !reflect.DeepEqual(first, second) || first.OperationID != request.OperationID || !filepath.IsAbs(first.Reference) {
		t.Fatalf("receipts = %#v and %#v", first, second)
	}
	file, err := os.Open(filepath.Join(first.Reference, request.Artifact.Name))
	if err != nil {
		t.Fatalf("open staged artifact: %v", err)
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(content, payload) {
		t.Fatalf("staged content = %q, %v", content, err)
	}
	manifest, err := os.ReadFile(filepath.Join(first.Reference, "release-manifest.json"))
	if err != nil || !bytes.Equal(manifest, request.SignedManifest) {
		t.Fatalf("staged manifest = %q, %v", manifest, err)
	}
}
