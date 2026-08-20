package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
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
	waitForSecondLevelActivity(t, browser, active, newest)
	items, err := browser.List(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "active" || !items[0].UpdatedAt.Equal(newest) {
		t.Fatalf("second-level file metadata was not used: %#v", items)
	}
}

func TestBrowserDoesNotScanThirdLevel(t *testing.T) {
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
	waitForSecondLevelActivity(t, browser, active, old)
	items, err := browser.List(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "quiet" {
		t.Fatalf("third-level file metadata changed directory order: %#v", items)
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

func TestBrowserSecondLevelScanDoesNotDelayList(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	private := filepath.Join(home, "Downloads")
	for _, path := range []string{project, private} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	browser, err := NewBrowser(home)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 1)
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	browser.scanSecondLevel = func(path string, latest time.Time) time.Time {
		calls.Add(1)
		started <- path
		<-release
		return latest
	}

	startedAt := time.Now()
	items, err := browser.List(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("second-level scan delayed list by %s", elapsed)
	}
	if len(items) != 2 {
		t.Fatalf("directories=%#v", items)
	}
	select {
	case path := <-started:
		canonicalProject, resolveErr := browser.Resolve(project)
		if resolveErr != nil || path != canonicalProject {
			t.Fatalf("scanned path=%q want project=%q err=%v", path, canonicalProject, resolveErr)
		}
	case <-time.After(time.Second):
		t.Fatal("background second-level scan did not start")
	}
	if calls.Load() != 1 {
		t.Fatalf("second-level scans=%d want=1", calls.Load())
	}
}

func waitForSecondLevelActivity(t *testing.T, browser *Browser, path string, want time.Time) {
	t.Helper()
	path, err := browser.Resolve(path)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got, found := browser.cachedSecondLevelActivity(path); found && got.Equal(want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	got, found := browser.cachedSecondLevelActivity(path)
	t.Fatalf("cached second-level activity for %q = %v, %t; want %v", path, got, found, want)
}

func BenchmarkBrowserListByProjectActivity(b *testing.B) {
	home := b.TempDir()
	for project := 0; project < 28; project++ {
		source := filepath.Join(home, fmt.Sprintf("project-%02d", project), "internal", "feature")
		if err := os.MkdirAll(source, 0o700); err != nil {
			b.Fatal(err)
		}
		for file := 0; file < 128; file++ {
			path := filepath.Join(source, fmt.Sprintf("file-%03d.go", file))
			if err := os.WriteFile(path, []byte("package feature\n"), 0o600); err != nil {
				b.Fatal(err)
			}
		}
	}
	browser, err := NewBrowser(home)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := browser.List(context.Background(), home); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSecondLevelActivityMetadata(b *testing.B) {
	home := b.TempDir()
	projects := make([]string, 0, 28)
	for project := 0; project < 28; project++ {
		path := filepath.Join(home, fmt.Sprintf("project-%02d", project))
		if err := os.Mkdir(path, 0o700); err != nil {
			b.Fatal(err)
		}
		projects = append(projects, path)
		for file := 0; file < 128; file++ {
			name := filepath.Join(path, fmt.Sprintf("file-%03d.go", file))
			if err := os.WriteFile(name, []byte("package feature\n"), 0o600); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ResetTimer()
	for range b.N {
		for _, project := range projects {
			latestSecondLevelActivity(project, time.Time{})
		}
	}
}
