package sessioncreation_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/sessioncreation"
)

func TestLocalBrowserListsRootsPagesDirectoriesAndCreatesChild(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Beta"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	browser, err := sessioncreation.NewLocalBrowser("local", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	roots, err := browser.Roots(context.Background(), "local")
	if err != nil || len(roots) != 1 || roots[0].Path != root {
		t.Fatalf("Roots() = %#v, %v", roots, err)
	}
	directories, err := browser.Browse(context.Background(), "local", root)
	if err != nil || len(directories) != 2 || directories[0].Name != "alpha" || directories[1].Name != "Beta" {
		t.Fatalf("Browse() = %#v, %v", directories, err)
	}
	created, err := browser.CreateChild(context.Background(), "local", root, "new")
	if err != nil || created != filepath.Join(root, "new") {
		t.Fatalf("CreateChild() = %q, %v", created, err)
	}
	again, err := browser.CreateChild(context.Background(), "local", root, "new")
	if err != nil || again != created {
		t.Fatalf("CreateChild(existing) = %q, %v", again, err)
	}
}

func TestLocalBrowserRejectsEscapeForeignComputerAndFileCollision(t *testing.T) {
	root := t.TempDir()
	browser, err := sessioncreation.NewLocalBrowser("local", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browser.Browse(context.Background(), "other", root); !errors.Is(err, sessioncreation.ErrUnavailablePath) {
		t.Fatalf("foreign computer error = %v", err)
	}
	if _, err := browser.Browse(context.Background(), "local", filepath.Dir(root)); !errors.Is(err, sessioncreation.ErrUnavailablePath) {
		t.Fatalf("escape error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "taken"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := browser.CreateChild(context.Background(), "local", root, "taken"); !errors.Is(err, sessioncreation.ErrInvalidName) {
		t.Fatalf("file collision error = %v", err)
	}
	for _, name := range []string{"", ".", "..", "a/b", " a"} {
		if _, err := browser.CreateChild(context.Background(), "local", root, name); !errors.Is(err, sessioncreation.ErrInvalidName) {
			t.Errorf("CreateChild(%q) error = %v", name, err)
		}
	}
}
