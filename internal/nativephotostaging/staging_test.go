package nativephotostaging_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/nativephotostaging"
)

func TestPhotoCustodyReopensWithoutChangingSourceAndRejectsTampering(t *testing.T) {
	root := t.TempDir()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "source")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data.Bytes()))
	size := int64(data.Len())
	if err := nativephotostaging.VerifyReference(path, size, digest); err != nil {
		t.Fatal(err)
	}
	dir := nativephotostaging.PersistentDirectory(root, "native-session")
	store := nativephotostaging.Store{Directory: dir}
	staged, err := store.Stage(context.Background(), path, size, digest)
	if err != nil {
		t.Fatal(err)
	}
	reopened := nativephotostaging.Store{Directory: dir}
	if got, err := reopened.Stage(context.Background(), path, size, digest); err != nil || got != staged {
		t.Fatalf("staging duplicated on reopen: %s %v", got, err)
	}
	info, err := os.Stat(staged)
	if err != nil || info.Mode().Perm() != 0400 {
		t.Fatal("staged file is not read-only/private")
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := nativephotostaging.VerifyReference(path, size, digest); err == nil {
		t.Fatal("changed source accepted")
	}
	if err := reopened.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatal("explicit cleanup retained staged file")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("cleanup removed source custody")
	}
	unsafe := nativephotostaging.Store{Directory: root}
	if err := unsafe.Cleanup(); err == nil {
		t.Fatal("broad cleanup directory accepted")
	}
}
