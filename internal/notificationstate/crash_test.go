package notificationstate_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"bria/internal/notificationstate"
)

// Each child has an independent mutex registry. Exiting immediately after claim
// models a process death without any receipt or application cleanup.
func TestClaimCrashHelper(t *testing.T) {
	path := os.Getenv("BRIA_TEST_CLAIM_FILE")
	if path == "" {
		return
	}
	store, err := notificationstate.OpenFilePartReceiptStore(path)
	if err != nil {
		os.Exit(4)
	}
	won, err := store.ClaimPart(context.Background(), "op", "op:part:1-of-1")
	if err != nil {
		os.Exit(5)
	}
	if !won {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestSeparateProcessesClaimOnceAndCrashRetainsFence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	store := openStore(t, path)
	if _, err := store.GetOrCreatePlan(ctx, "op", hash, []string{"page"}, nil); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var winners atomic.Int64
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			command := exec.Command(os.Args[0], "-test.run=^TestClaimCrashHelper$")
			command.Env = append(os.Environ(), "BRIA_TEST_CLAIM_FILE="+path)
			err := command.Run()
			if err == nil {
				winners.Add(1)
				return
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 3 {
				t.Errorf("claim subprocess failed: %v", err)
			}
		}()
	}
	workers.Wait()
	if winners.Load() != 1 {
		t.Fatalf("cross-process winners = %d, want 1", winners.Load())
	}
	store = openStore(t, path)
	if won, err := store.ClaimPart(ctx, "op", "op:part:1-of-1"); err != nil || won {
		t.Fatalf("claim retried after process crash: %v, %v", won, err)
	}
}

func TestStoreBoundsSymlinksAndCancelledCreation(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "parts.json")
		store := openStore(t, path)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := store.GetOrCreatePlan(cancelled, "op", hash, []string{"page"}, nil); err == nil {
			t.Fatal("cancelled plan creation accepted")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("cancelled creation persisted a plan")
		}
	})
	for _, target := range []string{"state", "lock"} {
		t.Run(target+" symlink", func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "parts.json")
			sentinel := filepath.Join(dir, "sentinel")
			if err := os.WriteFile(sentinel, []byte("unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := path
			if target == "lock" {
				link += ".lock"
			}
			if err := os.Symlink(sentinel, link); err != nil {
				t.Fatal(err)
			}
			if _, err := notificationstate.OpenFilePartReceiptStore(path); err == nil {
				t.Fatal("symlink accepted")
			}
			got, err := os.ReadFile(sentinel)
			if err != nil || string(got) != "unchanged" {
				t.Fatal("symlink target modified")
			}
		})
	}
	t.Run("oversize", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "parts.json")
		fixture := `{"version":1,"operations":{"op":{}}}`
		if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
			t.Fatal(err)
		}
		store := openStore(t, path)
		if _, err := store.GetOrCreatePlan(ctx, "op", hash, nil, []string{strings.Repeat("x", (8<<20)+1)}); err == nil {
			t.Fatal("oversized legacy plan accepted")
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != fixture {
			t.Fatal("failed oversized upgrade changed v1")
		}
	})
}
