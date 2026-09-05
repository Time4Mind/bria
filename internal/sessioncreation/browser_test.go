package sessioncreation_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	tie := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	for _, name := range []string{"Beta", "alpha"} {
		if err := os.Chtimes(filepath.Join(root, name), tie, tie); err != nil {
			t.Fatal(err)
		}
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
	home, err := browser.Home(context.Background(), "local")
	if err != nil || home != root {
		t.Fatalf("Home() = %q, %v", home, err)
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

func TestLocalBrowserSortsDirectoriesByNewestRecursiveContent(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "alpha-stale")
	recent := filepath.Join(root, "zulu-recent")
	recentNested := filepath.Join(recent, "nested")
	recentLeaf := filepath.Join(recentNested, "leaf")
	for _, directory := range []string{stale, recent, recentNested, recentLeaf} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	newTime := oldTime.Add(24 * time.Hour)
	staleFile := filepath.Join(stale, "old.txt")
	recentFile := filepath.Join(recentLeaf, "new.txt")
	for _, path := range []string{staleFile, recentFile} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	projectMarker := filepath.Join(recent, "go.mod")
	if err := os.WriteFile(projectMarker, []byte("module example.test/recent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{stale, recent, recentNested, recentLeaf, staleFile, projectMarker} {
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(recentFile, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	browser, err := sessioncreation.NewLocalBrowser("local", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	browser.Start(context.Background())
	t.Cleanup(browser.Close)
	deadline := time.Now().Add(2 * time.Second)
	for {
		directories, browseErr := browser.Browse(context.Background(), "local", root)
		if browseErr == nil && len(directories) == 2 && directories[0].Name == "zulu-recent" && directories[1].Name == "alpha-stale" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Browse() never observed warmed activity order: %#v, %v", directories, browseErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLocalBrowserDefaultsToUserHomeWithoutRestrictingFilesystemRoot(t *testing.T) {
	browser, err := sessioncreation.NewLocalBrowser("local", nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(mustUserHome(t))
	if err != nil {
		t.Fatal(err)
	}
	home, err := browser.Home(context.Background(), "local")
	if err != nil || home != want {
		t.Fatalf("Home() = %q, %v, want %q", home, err, want)
	}
	parent, ok := browser.Parent(home)
	if !ok || parent != filepath.Dir(home) {
		t.Fatalf("home parent = %q, %t", parent, ok)
	}
}

func mustUserHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return home
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
