package screen_test

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"reflect"
	"strings"
	"sync"
	"testing"

	"bria/internal/domain"
	"bria/internal/screen"
	"bria/internal/sessionruntime"
)

func TestTypedEventAdapterSanitizesTerminalControlsAndBoundsVisibleLines(t *testing.T) {
	store := newStore(t, screen.Options{MaxSessions: 4, MaxLines: 3, MaxColumns: 12, MaxEventBytes: 256, MaxPNGBytes: 64 << 10})
	adapter, err := store.Events(domain.SessionID("session-a"))
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	events := []sessionruntime.TurnEvent{
		{Kind: sessionruntime.EventCommentary, Text: "first\x1b[31m RED\x1b[0m\x00"},
		{Kind: sessionruntime.EventQuestion, Text: "second\rOVER\nthird"},
		{Kind: sessionruntime.EventCommentary, Text: "fourth"},
	}
	for _, event := range events {
		if err := adapter.Handle(context.Background(), event); err != nil {
			t.Fatalf("Handle(%#v) error = %v", event, err)
		}
	}
	snapshot, err := store.Snapshot(context.Background(), domain.SessionID("session-a"))
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Revision != uint64(len(events)) {
		t.Fatalf("revision = %d", snapshot.Revision)
	}
	if len(snapshot.Lines) != 3 || strings.Contains(strings.Join(snapshot.Lines, ""), "\x1b") || strings.Contains(strings.Join(snapshot.Lines, ""), "\x00") {
		t.Fatalf("sanitized bounded lines = %#v", snapshot.Lines)
	}
	if !reflect.DeepEqual(snapshot.Lines, []string{"OVER", "third", "fourth"}) {
		t.Fatalf("lines = %#v", snapshot.Lines)
	}
}

func TestSnapshotIsDeterministicBoundedMonochromePNG(t *testing.T) {
	store := newStore(t, screen.Options{MaxSessions: 2, MaxLines: 4, MaxColumns: 16, MaxEventBytes: 256, MaxPNGBytes: 64 << 10})
	adapter, err := store.Events(domain.SessionID("session-b"))
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if err := adapter.Handle(context.Background(), sessionruntime.TurnEvent{Kind: sessionruntime.EventCommentary, Text: "Build 42 OK"}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	first, err := store.Snapshot(context.Background(), domain.SessionID("session-b"))
	if err != nil {
		t.Fatalf("first Snapshot() error = %v", err)
	}
	second, err := store.Snapshot(context.Background(), domain.SessionID("session-b"))
	if err != nil {
		t.Fatalf("second Snapshot() error = %v", err)
	}
	if !bytes.Equal(first.PNG, second.PNG) || len(first.PNG) == 0 || len(first.PNG) > 64<<10 {
		t.Fatalf("PNG determinism/size = equal %v, bytes %d", bytes.Equal(first.PNG, second.PNG), len(first.PNG))
	}
	decoded, err := png.Decode(bytes.NewReader(first.PNG))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	if decoded.Bounds().Dx() != first.Width || decoded.Bounds().Dy() != first.Height || first.Width <= 0 || first.Height <= 0 {
		t.Fatalf("PNG bounds = %v, metadata %dx%d", decoded.Bounds(), first.Width, first.Height)
	}
	for y := decoded.Bounds().Min.Y; y < decoded.Bounds().Max.Y; y++ {
		for x := decoded.Bounds().Min.X; x < decoded.Bounds().Max.X; x++ {
			r, g, b, _ := decoded.At(x, y).RGBA()
			if !((r == 0 && g == 0 && b == 0) || (r == 0xffff && g == 0xffff && b == 0xffff)) {
				t.Fatalf("non-monochrome pixel at %d,%d = %d,%d,%d", x, y, r, g, b)
			}
		}
	}
}

func TestOversizedOrUnknownTypedEventDoesNotMutateScreen(t *testing.T) {
	store := newStore(t, screen.Options{MaxSessions: 2, MaxLines: 4, MaxColumns: 16, MaxEventBytes: 8, MaxPNGBytes: 64 << 10})
	adapter, err := store.Events(domain.SessionID("session-c"))
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if err := adapter.Handle(context.Background(), sessionruntime.TurnEvent{Kind: sessionruntime.EventCommentary, Text: "123456789"}); !strings.Contains(errString(err), "event") {
		t.Fatalf("oversized Handle() error = %v", err)
	}
	if err := adapter.Handle(context.Background(), sessionruntime.TurnEvent{Kind: sessionruntime.EventKind("auth_secret"), Text: "secret"}); !strings.Contains(errString(err), "event") {
		t.Fatalf("unknown Handle() error = %v", err)
	}
	snapshot, err := store.Snapshot(context.Background(), domain.SessionID("session-c"))
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Revision != 0 || len(snapshot.Lines) != 0 {
		t.Fatalf("rejected events mutated screen: %#v", snapshot)
	}
}

func TestConcurrentSessionsHaveAtomicIndependentSnapshots(t *testing.T) {
	store := newStore(t, screen.Options{MaxSessions: 8, MaxLines: 64, MaxColumns: 32, MaxEventBytes: 128, MaxPNGBytes: 128 << 10})
	const sessions = 6
	const events = 40
	var workers sync.WaitGroup
	for sessionIndex := 0; sessionIndex < sessions; sessionIndex++ {
		id := domain.SessionID(string(rune('a' + sessionIndex)))
		adapter, err := store.Events(id)
		if err != nil {
			t.Fatalf("Events(%q) error = %v", id, err)
		}
		workers.Add(1)
		go func(id domain.SessionID, adapter screen.EventAdapter) {
			defer workers.Done()
			for eventIndex := 0; eventIndex < events; eventIndex++ {
				if err := adapter.Handle(context.Background(), sessionruntime.TurnEvent{Kind: sessionruntime.EventCommentary, Text: string(id)}); err != nil {
					t.Errorf("Handle(%q) error = %v", id, err)
					return
				}
				if _, err := store.Snapshot(context.Background(), id); err != nil {
					t.Errorf("Snapshot(%q) error = %v", id, err)
					return
				}
			}
		}(id, adapter)
	}
	workers.Wait()
	for sessionIndex := 0; sessionIndex < sessions; sessionIndex++ {
		id := domain.SessionID(string(rune('a' + sessionIndex)))
		snapshot, err := store.Snapshot(context.Background(), id)
		if err != nil {
			t.Fatalf("Snapshot(%q) error = %v", id, err)
		}
		if snapshot.Revision != events || len(snapshot.Lines) != events {
			t.Fatalf("Snapshot(%q) = revision %d lines %d", id, snapshot.Revision, len(snapshot.Lines))
		}
		for _, line := range snapshot.Lines {
			if line != string(id) {
				t.Fatalf("cross-session line in %q: %q", id, line)
			}
		}
	}
}

func TestSnapshotExposesImmutableTelegramPhotoPayload(t *testing.T) {
	store := newStore(t, screen.Options{MaxSessions: 2, MaxLines: 4, MaxColumns: 16, MaxEventBytes: 64, MaxPNGBytes: 64 << 10})
	adapter, _ := store.Events(domain.SessionID("session-media"))
	if err := adapter.Handle(context.Background(), sessionruntime.TurnEvent{Kind: sessionruntime.EventQuestion, Text: "Approve?"}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	snapshot, err := store.Snapshot(context.Background(), domain.SessionID("session-media"))
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	media := snapshot.TelegramMedia()
	if media.ContentType != "image/png" || media.FileName == "" || strings.Contains(media.FileName, "session-media") || !bytes.Equal(media.Content, snapshot.PNG) {
		t.Fatalf("TelegramMedia() = %#v", media)
	}
	media.Content[0] ^= 0xff
	if bytes.Equal(media.Content, snapshot.PNG) {
		t.Fatal("Telegram media aliases snapshot bytes")
	}
}

func TestRemoveReleasesBoundedSessionSlotWithoutAffectingAnotherSession(t *testing.T) {
	store := newStore(t, screen.Options{MaxSessions: 1, MaxLines: 2, MaxColumns: 8, MaxEventBytes: 32, MaxPNGBytes: 32 << 10})
	if _, err := store.Events(domain.SessionID("old")); err != nil {
		t.Fatalf("Events(old) error = %v", err)
	}
	if _, err := store.Events(domain.SessionID("blocked")); !errors.Is(err, screen.ErrSessionLimit) {
		t.Fatalf("Events(over limit) error = %v", err)
	}
	if err := store.Remove(context.Background(), domain.SessionID("old")); err != nil {
		t.Fatalf("Remove(old) error = %v", err)
	}
	if _, err := store.Events(domain.SessionID("new")); err != nil {
		t.Fatalf("Events(new) error = %v", err)
	}
	if _, err := store.Snapshot(context.Background(), domain.SessionID("old")); !errors.Is(err, screen.ErrUnknownSession) {
		t.Fatalf("Snapshot(removed) error = %v", err)
	}
}

func newStore(t *testing.T, options screen.Options) *screen.Store {
	t.Helper()
	store, err := screen.New(options)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return store
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
