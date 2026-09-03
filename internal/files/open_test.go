package files_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/files"
)

func TestOpenRegularRejectsTraversalAndSymlinks(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "allowed")
	outside := filepath.Join(parent, "outside.txt")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(root, "report.txt")
	if err := os.WriteFile(regular, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(root, "file-link")
	if err := os.Symlink(regular, fileLink); err != nil {
		t.Fatal(err)
	}
	directoryLink := filepath.Join(root, "dir-link")
	if err := os.Symlink(parent, directoryLink); err != nil {
		t.Fatal(err)
	}

	opener := files.Opener{AllowedRoots: []string{root}, MaxBytes: 1024}
	for name, candidate := range map[string]string{
		"lexical traversal": filepath.Join(root, "..", "outside.txt"),
		"file symlink":      fileLink,
		"directory symlink": filepath.Join(directoryLink, "outside.txt"),
		"directory":         root,
	} {
		t.Run(name, func(t *testing.T) {
			opened, err := opener.OpenRegular(candidate)
			if opened != nil {
				_ = opened.Close()
			}
			if !errors.Is(err, files.ErrPathNotAllowed) && !errors.Is(err, files.ErrNotRegular) {
				t.Fatalf("error = %v, want a safe rejection", err)
			}
		})
	}
}

func TestOpenRegularReturnsDescriptorThatSurvivesPathReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "result.txt")
	if err := os.WriteFile(path, []byte("verified"), 0o600); err != nil {
		t.Fatal(err)
	}
	opener := files.Opener{AllowedRoots: []string{root}, MaxBytes: 1024}

	opened, err := opener.OpenRegular(path)
	if err != nil {
		t.Fatalf("open regular file: %v", err)
	}
	defer opened.Close()
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	content, err := io.ReadAll(opened)
	if err != nil {
		t.Fatalf("read opened file: %v", err)
	}
	if string(content) != "verified" {
		t.Fatalf("opened content = %q, want original descriptor", content)
	}
	if opened.Path != path || opened.Size != int64(len("verified")) {
		t.Fatalf("metadata = (%q, %d)", opened.Path, opened.Size)
	}
}

func TestOpenRegularRejectsOversizeFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.bin")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}

	opened, err := (files.Opener{AllowedRoots: []string{root}, MaxBytes: 4}).OpenRegular(path)
	if opened != nil {
		_ = opened.Close()
	}
	if !errors.Is(err, files.ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
}

func TestOpenRegularBoundsStreamIfFileGrowsAfterOpen(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "growing.bin")
	if err := os.WriteFile(path, []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := (files.Opener{AllowedRoots: []string{root}, MaxBytes: 4}).OpenRegular(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	writer, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString("5"); err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	content, err := io.ReadAll(opened)
	if !errors.Is(err, files.ErrTooLarge) {
		t.Fatalf("read error = %v, want ErrTooLarge", err)
	}
	if string(content) != "1234" {
		t.Fatalf("bounded content = %q", content)
	}
}
