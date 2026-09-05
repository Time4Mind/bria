package sessioncreation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
)

func TestBrowseDoesNotWaitForBackgroundActivityScan(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "project")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	browser, err := NewLocalBrowser(domain.ComputerID("local"), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	browser.activity = newDirectoryActivityIndexWithScanner(func(ctx context.Context, _ string) (time.Time, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-release:
			return time.Now(), nil
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		}
	})
	browser.Start(context.Background())
	defer browser.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background scan did not start")
	}

	result := make(chan error, 1)
	go func() {
		_, browseErr := browser.Browse(context.Background(), domain.ComputerID("local"), root)
		result <- browseErr
	}()
	select {
	case browseErr := <-result:
		if browseErr != nil {
			t.Fatal(browseErr)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("Browse waited for the background activity scan")
	}
	close(release)
}
