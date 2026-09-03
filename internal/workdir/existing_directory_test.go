package workdir_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/app"
	"bria/internal/workdir"
)

func TestExistingDirectoryAcceptsAbsoluteDirectoryAndSymlinkToDirectory(t *testing.T) {
	t.Parallel()

	realDirectory := t.TempDir()
	alias := filepath.Join(t.TempDir(), "directory-alias")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Fatalf("create directory symlink: %v", err)
	}
	var validator app.WorkdirValidator = workdir.ExistingDirectory{}
	for _, path := range []string{realDirectory, alias} {
		if err := validator.Validate(context.Background(), path); err != nil {
			t.Errorf("Validate(%q) error = %v", path, err)
		}
	}
}

func TestExistingDirectoryRejectsInvalidMissingFileAndRelativePaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "regular-file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create regular file: %v", err)
	}
	tests := []struct {
		name string
		path string
	}{
		{name: "invalid empty", path: ""},
		{name: "missing", path: filepath.Join(root, "missing")},
		{name: "regular file", path: file},
		{name: "relative", path: "relative/workdir"},
	}

	validator := workdir.ExistingDirectory{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validator.Validate(context.Background(), test.path); err == nil {
				t.Fatalf("Validate(%q) error = nil", test.path)
			}
		})
	}
}

func TestExistingDirectoryHonorsCanceledContextBeforeFilesystemAccess(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (workdir.ExistingDirectory{}).Validate(ctx, "/definitely/not/present")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate() error = %v, want context.Canceled", err)
	}
}
