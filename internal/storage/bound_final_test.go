package storage_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/storage"
)

func saveBoundFinal(s *storage.SessionStore, ctx context.Context, expected domain.Session, message, final string) (bool, error) {
	return s.RestoreAcceptedFinalForSession(ctx, expected, message, final)
}

func boundFinalStore(t *testing.T) (*storage.SessionStore, string, domain.Session) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	starting := mustStartingSession(t, "bound-final", "bound-final-intent")
	if _, _, err := s.PutStartingIfAbsent(ctx, starting); err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: starting.Provider(), SessionID: "provider-bound", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(ctx, starting, ready); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"A", "B"} {
		if err := s.SetCardPrompt(ctx, ready.ID(), message, message); err != nil {
			t.Fatal(err)
		}
	}
	return s, path, ready
}

func TestBoundFinalRejectsStaleSessionWithoutMutation(t *testing.T) {
	for _, change := range []string{"generation", "status", "name", "missing"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s, path, expected := boundFinalStore(t)
			current := expected
			snapshot := expected.Snapshot()
			switch change {
			case "generation":
				snapshot.Binding.Generation++
			case "status":
				snapshot.Status = domain.SessionRunning
			case "name":
				snapshot.Name = "Renamed"
			case "missing":
				expected = mustStartingSession(t, "absent", "absent-intent")
			}
			if change != "missing" {
				var err error
				current, err = domain.RestoreSession(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				other, err := storage.OpenSessionStore(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := other.Replace(ctx, expected, current); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			ok, err := saveBoundFinal(s, ctx, expected, "A", "old final")
			if ok || err != nil {
				t.Fatalf("stale final accepted=%v err=%v; want false,nil", ok, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			afterInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || !os.SameFile(info, afterInfo) || !info.ModTime().Equal(afterInfo.ModTime()) {
				t.Fatal("stale save mutated physical state")
			}
			got, err := s.Load(ctx, current.ID())
			if err != nil || !got.Equal(current) {
				t.Fatal("replacement session changed")
			}
		})
	}
}

func TestBoundFinalIsIdempotentAcrossReopenAndRejectsConflict(t *testing.T) {
	ctx := context.Background()
	s, path, expected := boundFinalStore(t)
	final := strings.Repeat("🙂", 5000)
	for range 2 {
		ok, err := saveBoundFinal(s, ctx, expected, "A", final)
		if !ok || err != nil {
			t.Fatalf("save=%v,%v", ok, err)
		}
		s, err = storage.OpenSessionStore(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	blocks, err := s.LoadCardTranscript(ctx, expected.ID(), true)
	if err != nil {
		t.Fatal(err)
	}
	var saved strings.Builder
	for _, block := range blocks {
		if block.Kind == "final" {
			saved.WriteString(block.Text)
		}
	}
	if saved.String() != final || blocks[0].Text != "A" || blocks[len(blocks)-1].Text != "B" {
		t.Fatal("exact final or queued prompt changed after reopen")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := saveBoundFinal(s, ctx, expected, "A", "conflicting final"); ok || err == nil {
		t.Fatal("conflicting final accepted")
	}
	if ok, err := saveBoundFinal(s, ctx, expected, "absent", "final"); ok || err == nil {
		t.Fatal("missing anchor accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if ok, err := saveBoundFinal(s, canceled, expected, "A", final); ok || err != context.Canceled {
		t.Fatalf("canceled save=%v,%v", ok, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected save changed state")
	}
}

func TestBoundFinalConcurrentReplacementIsAtomic(t *testing.T) {
	ctx := context.Background()
	for range 30 {
		s, path, expected := boundFinalStore(t)
		other, err := storage.OpenSessionStore(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot := expected.Snapshot()
		snapshot.Binding.Generation++
		next, err := domain.RestoreSession(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		type result struct {
			saved bool
			err   error
		}
		saved := make(chan result, 1)
		replaced := make(chan result, 1)
		go func() {
			<-start
			ok, err := saveBoundFinal(s, ctx, expected, "A", "exact final")
			saved <- result{ok, err}
		}()
		go func() {
			<-start
			if err := other.Replace(ctx, expected, next); err != nil {
				replaced <- result{err: err}
				return
			}
			blocks, err := other.LoadCardTranscript(ctx, next.ID(), true)
			seen := false
			for _, block := range blocks {
				seen = seen || block.Kind == "final"
			}
			replaced <- result{seen, err}
		}()
		close(start)
		got := <-saved
		observed := <-replaced
		if observed.err != nil {
			t.Fatal(observed.err)
		}
		if got.err != nil {
			t.Fatal(got.err)
		}
		if observed.saved != got.saved {
			t.Fatal("final appeared after replacement instead of atomically before it")
		}
		reopened, err := storage.OpenSessionStore(path)
		if err != nil {
			t.Fatal(err)
		}
		current, err := reopened.Load(ctx, next.ID())
		if err != nil || !current.Equal(next) {
			t.Fatal("replacement lost")
		}
		blocks, err := reopened.LoadCardTranscript(ctx, next.ID(), true)
		if err != nil {
			t.Fatal(err)
		}
		finals := 0
		for _, block := range blocks {
			if block.Kind == "final" {
				finals++
				if block.Text != "exact final" {
					t.Fatal("wrong final")
				}
			}
		}
		want := 0
		if got.saved {
			want = 1
		}
		if finals != want {
			t.Fatalf("save receipt=%v final count=%d", got.saved, finals)
		}
		if ok, err := saveBoundFinal(reopened, ctx, expected, "A", "late stale final"); ok || err != nil {
			t.Fatalf("post-replacement save=%v,%v", ok, err)
		}
	}
}

func TestBoundFinalWriteErrorDoesNotClaimDurability(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged user")
	}
	ctx := context.Background()
	s, path, expected := boundFinalStore(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Error(err)
		}
	})
	if ok, err := saveBoundFinal(s, ctx, expected, "A", "exact final"); ok || err == nil {
		t.Fatalf("write error claimed durability=%v,%v", ok, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed save changed durable state")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if ok, err := saveBoundFinal(s, ctx, expected, "A", "exact final"); !ok || err != nil {
		t.Fatalf("retry failed=%v,%v", ok, err)
	}
}
