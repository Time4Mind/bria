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

func TestBrowserActivityScanDoesNotDelayList(t *testing.T) {
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
	browser.scanActivity = func(path string, latest time.Time) time.Time {
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
		t.Fatalf("activity scan delayed list by %s", elapsed)
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
		t.Fatal("background activity scan did not start")
	}
	if calls.Load() != 1 {
		t.Fatalf("activity scans=%d want=1", calls.Load())
	}
}

func waitForActivity(t *testing.T, browser *Browser, path string, want time.Time) {
	t.Helper()
	path, err := browser.Resolve(path)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got, found := browser.cachedActivity(path); found && got.Equal(want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	got, found := browser.cachedActivity(path)
	t.Fatalf("cached activity for %q = %v, %t; want %v", path, got, found, want)
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

func BenchmarkThirdLevelActivityMetadata(b *testing.B) {
	home := b.TempDir()
	projects := make([]string, 0, 28)
	for project := 0; project < 28; project++ {
		path := filepath.Join(home, fmt.Sprintf("project-%02d", project))
		if err := os.Mkdir(path, 0o700); err != nil {
			b.Fatal(err)
		}
		projects = append(projects, path)
		for directory := 0; directory < 16; directory++ {
			source := filepath.Join(path, fmt.Sprintf("dir-%02d", directory))
			if err := os.Mkdir(source, 0o700); err != nil {
				b.Fatal(err)
			}
			for file := 0; file < 7; file++ {
				name := filepath.Join(source, fmt.Sprintf("file-%02d.go", file))
				if err := os.WriteFile(name, []byte("package feature\n"), 0o600); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	b.ResetTimer()
	for range b.N {
		for _, project := range projects {
			latestThirdLevelActivity(project, time.Time{})
		}
	}
}
