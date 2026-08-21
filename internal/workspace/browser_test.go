package workspace

import (
	"context"
	"fmt"
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
	items, err := browser.List(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "new" || items[1].Name != "old" {
		t.Fatalf("directories=%#v", items)
	}
}

func TestBrowserUsesCachedSecondLevelMetadata(t *testing.T) {
	home := t.TempDir()
	active := filepath.Join(home, "active")
	quiet := filepath.Join(home, "quiet")
	for _, path := range []string{active, quiet} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Unix(100, 0)
	middle := time.Unix(200, 0)
	newest := time.Unix(300, 0)
	file := filepath.Join(active, "feature.go")
	if err := os.WriteFile(file, []byte("package feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(active, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(quiet, middle, middle); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, newest, newest); err != nil {
		t.Fatal(err)
	}

	browser, err := NewBrowser(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browser.List(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	waitForActivity(t, browser, active, newest)
	items, err := browser.List(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "active" || !items[0].UpdatedAt.Equal(newest) {
		t.Fatalf("second-level file metadata was not used: %#v", items)
	}
}

func TestBrowserUsesCachedThirdLevelMetadata(t *testing.T) {
	home := t.TempDir()
	active := filepath.Join(home, "active")
	quiet := filepath.Join(home, "quiet")
	secondLevel := filepath.Join(active, "internal")
	for _, path := range []string{secondLevel, quiet} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Unix(100, 0)
	middle := time.Unix(200, 0)
	newest := time.Unix(300, 0)
	file := filepath.Join(secondLevel, "feature.go")
	if err := os.WriteFile(file, []byte("package feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{active, secondLevel} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(quiet, middle, middle); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, newest, newest); err != nil {
		t.Fatal(err)
	}

	browser, err := NewBrowser(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browser.List(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	waitForActivity(t, browser, active, newest)
	items, err := browser.List(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "active" || !items[0].UpdatedAt.Equal(newest) {
		t.Fatalf("third-level file metadata was not used: %#v", items)
	}
}

func TestBrowserDoesNotScanFourthLevel(t *testing.T) {
	home := t.TempDir()
	active := filepath.Join(home, "active")
	quiet := filepath.Join(home, "quiet")
	secondLevel := filepath.Join(active, "internal")
	thirdLevel := filepath.Join(secondLevel, "feature")
	for _, path := range []string{thirdLevel, quiet} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Unix(100, 0)
	middle := time.Unix(200, 0)
	newest := time.Unix(300, 0)
	file := filepath.Join(thirdLevel, "feature.go")
	if err := os.WriteFile(file, []byte("package feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{active, secondLevel, thirdLevel} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(quiet, middle, middle); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, newest, newest); err != nil {
		t.Fatal(err)
	}

	browser, err := NewBrowser(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browser.List(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	waitForActivity(t, browser, active, old)
	items, err := browser.List(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "quiet" {
		t.Fatalf("fourth-level file metadata changed directory order: %#v", items)
	}
}

func TestBrowserIgnoresGeneratedActivityTrees(t *testing.T) {
	home := t.TempDir()
	generated := filepath.Join(home, "generated")
	quiet := filepath.Join(home, "quiet")
	build := filepath.Join(generated, "build")
	for _, path := range []string{build, quiet} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Unix(100, 0)
	middle := time.Unix(200, 0)
	newest := time.Unix(300, 0)
	artifact := filepath.Join(build, "artifact.bin")
	if err := os.WriteFile(artifact, []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{generated, build} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(quiet, middle, middle); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(artifact, newest, newest); err != nil {
		t.Fatal(err)
	}

	browser, err := NewBrowser(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browser.List(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	waitForActivity(t, browser, generated, old)
	items, err := browser.List(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "quiet" {
		t.Fatalf("generated artifacts changed project order: %#v", items)
	}
}

func TestBrowserSelectsCandidatesBeforeApplyingEntryLimit(t *testing.T) {
	home := t.TempDir()
	old := time.Unix(100, 0)
	for index := 0; index < MaxEntries; index++ {
		path := filepath.Join(home, fmt.Sprintf("project-%03d", index))
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	latest := filepath.Join(home, "zz-latest")
	if err := os.Mkdir(latest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(latest, time.Unix(300, 0), time.Unix(300, 0)); err != nil {
		t.Fatal(err)
	}

	browser, err := NewBrowser(home)
	if err != nil {
		t.Fatal(err)
	}
	items, err := browser.List(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || items[0].Name != "zz-latest" {
		t.Fatalf("latest directory was excluded before sorting: %#v", items)
	}
}

func TestBrowserRejectsRelativeAndNondirectoryPaths(t *testing.T) {
	browser, err := NewBrowser(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browser.List(context.Background(), "relative"); err == nil {
		t.Fatal("relative path accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := browser.List(context.Background(), file); err == nil {
		t.Fatal("regular file accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := browser.List(canceled, browser.Home()); err == nil {
		t.Fatal("canceled context accepted")
	}
}
