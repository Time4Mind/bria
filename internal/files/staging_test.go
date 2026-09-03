package files_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/files"
)

func TestStagerCreatesRestrictedRandomFileAndCleansIt(t *testing.T) {
	directory := t.TempDir()
	stager := files.Stager{Directory: directory, MaxBytes: 16}

	first, err := stager.Stage(strings.NewReader("voice-one"))
	if err != nil {
		t.Fatalf("stage first stream: %v", err)
	}
	second, err := stager.Stage(strings.NewReader("voice-two"))
	if err != nil {
		t.Fatalf("stage second stream: %v", err)
	}
	t.Cleanup(func() { _ = first.Cleanup(); _ = second.Cleanup() })

	if first.Path() == second.Path() {
		t.Fatal("staged streams reused a filename")
	}
	if filepath.Dir(first.Path()) != directory {
		t.Fatalf("staged path %q is outside %q", first.Path(), directory)
	}
	info, err := os.Lstat(first.Path())
	if err != nil {
		t.Fatalf("stat staged file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %#o, want 0600", got)
	}
	content, err := os.ReadFile(first.Path())
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if !bytes.Equal(content, []byte("voice-one")) {
		t.Fatalf("content = %q", content)
	}

	if err := first.Cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if err := first.Cleanup(); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if _, err := os.Lstat(first.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file remains after cleanup: %v", err)
	}
}

func TestStagerRejectsOversizeAndRemovesPartialFile(t *testing.T) {
	directory := t.TempDir()
	stager := files.Stager{Directory: directory, MaxBytes: 4}

	_, err := stager.Stage(strings.NewReader("12345"))
	if !errors.Is(err, files.ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		t.Fatalf("read staging directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("partial files remain: %v", entries)
	}
}
