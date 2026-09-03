package files_test

import (
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/files"
)

func TestExtractFinalLinksUsesStrictDeterministicSyntax(t *testing.T) {
	first := "/tmp/report one.csv"
	second := "/tmp/archive.zip"
	final := "result [report](file://" + (&url.URL{Path: first}).EscapedPath() + ") " +
		"and <file://" + (&url.URL{Path: second}).EscapedPath() + "> " +
		"duplicate [again](file://" + (&url.URL{Path: first}).EscapedPath() + ") " +
		"ignored /tmp/bare.txt https://example.test/file and [relative](file:relative.txt)"

	links, err := files.ExtractFinalLinks(final)
	if err != nil {
		t.Fatalf("extract final links: %v", err)
	}
	want := []files.Link{{Path: first}, {Path: second}}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("links = %#v, want %#v", links, want)
	}
}

func TestExtractFinalLinksRejectsNonLocalAuthorities(t *testing.T) {
	_, err := files.ExtractFinalLinks("[bad](file://remote-host/tmp/report.csv)")
	if !errors.Is(err, files.ErrInvalidFileLink) {
		t.Fatalf("error = %v, want ErrInvalidFileLink", err)
	}
}

func TestOpenFinalFilesVerifiesAndReopensEveryLink(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.txt")
	secondPath := filepath.Join(root, "second.txt")
	if err := os.WriteFile(firstPath, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	final := "[one](file://" + (&url.URL{Path: firstPath}).EscapedPath() + ") " +
		"<file://" + (&url.URL{Path: secondPath}).EscapedPath() + ">"

	opened, err := files.OpenFinalFiles(final, files.Opener{AllowedRoots: []string{root}, MaxBytes: 64})
	if err != nil {
		t.Fatalf("open final files: %v", err)
	}
	defer func() {
		for _, file := range opened {
			_ = file.Close()
		}
	}()
	if len(opened) != 2 {
		t.Fatalf("opened count = %d, want 2", len(opened))
	}
	for index, want := range []string{"one", "two"} {
		content, readErr := io.ReadAll(opened[index])
		if readErr != nil {
			t.Fatalf("read file %d: %v", index, readErr)
		}
		if string(content) != want {
			t.Fatalf("file %d = %q, want %q", index, content, want)
		}
	}
}

func TestOpenFinalFilesClosesEarlierDescriptorsOnLaterFailure(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "valid.txt")
	if err := os.WriteFile(valid, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	final := "[ok](file://" + (&url.URL{Path: valid}).EscapedPath() + ") " +
		"[outside](file://" + (&url.URL{Path: outside}).EscapedPath() + ")"

	opened, err := files.OpenFinalFiles(final, files.Opener{AllowedRoots: []string{root}, MaxBytes: 64})
	if opened != nil || !errors.Is(err, files.ErrPathNotAllowed) {
		t.Fatalf("opened = %#v, error = %v", opened, err)
	}
}
