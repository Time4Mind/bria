package workspace

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBrowserListsRealVisibleDirectoriesByRecency(t *testing.T) {
	home := t.TempDir()
	oldPath := filepath.Join(home, "old")
	newPath := filepath.Join(home, "new")
	for _, path := range []string{oldPath, newPath, filepath.Join(home, ".hidden")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Unix(100, 0)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newPath, filepath.Join(home, "linked")); err != nil {
		t.Fatal(err)
	}
	browser, err := NewBrowser(home)
	if err != nil {
		t.Fatal(err)
	}
	items, err := browser.List(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "new" || items[1].Name != "old" {
		t.Fatalf("directories=%#v", items)
	}
}

func TestBrowserRejectsRelativeAndNondirectoryPaths(t *testing.T) {
	browser, err := NewBrowser(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browser.List("relative"); err == nil {
		t.Fatal("relative path accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := browser.List(file); err == nil {
		t.Fatal("regular file accepted")
	}
}
