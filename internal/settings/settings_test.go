package settings

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestDefaultsAreProductDefaults(t *testing.T) {
	s := Default()
	if !s.ContinueExisting || s.ScreenEnabled || !s.ShowTechnicalActions || !s.NotifyBackgroundQuestions || !s.NotifyBackgroundErrors {
		t.Fatalf("unexpected boolean defaults: %+v", s)
	}
	if s.CardDetail != CardDetailStandard || s.SessionLifetime != LifetimeNever || s.VoiceRecognition != VoiceParakeet || s.QueueLimit != DefaultQueueLimit || s.RetryUndeliveredFiles {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("default validation: %v", err)
	}
}

func TestValidateRejectsUnsupportedValues(t *testing.T) {
	cases := []Settings{
		func() Settings { s := Default(); s.CardDetail = "verbose"; return s }(),
		func() Settings { s := Default(); s.SessionLifetime = "3h"; return s }(),
		func() Settings { s := Default(); s.VoiceRecognition = "cloud"; return s }(),
		func() Settings { s := Default(); s.QueueLimit = 0; return s }(),
	}
	for i, s := range cases {
		if err := s.Validate(); err == nil {
			t.Errorf("case %d validation succeeded", i)
		}
	}
}

func TestFileStoreRoundTripAndAtomicPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(context.Background()); err != nil || got != Default() {
		t.Fatalf("initial load = %+v, %v", got, err)
	}
	if err := store.Update(context.Background(), func(s *Settings) error { s.ScreenEnabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.ScreenEnabled || !got.ContinueExisting {
		t.Fatalf("round trip lost values: %+v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("settings mode = %o, want 600", info.Mode().Perm())
	}
}

func TestUpdateRejectsInvalidWithoutMutation(t *testing.T) {
	store := NewMemoryStore()
	err := store.Update(context.Background(), func(s *Settings) error { s.SessionLifetime = "3h"; return nil })
	if err == nil {
		t.Fatal("invalid update succeeded")
	}
	got, _ := store.Load(context.Background())
	if got != Default() {
		t.Fatalf("invalid update mutated state: %+v", got)
	}
}

func TestUpdateHonorsCanceledContext(t *testing.T) {
	store := NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Update(ctx, func(*Settings) error { t.Fatal("mutator called"); return nil }); err == nil {
		t.Fatal("canceled update succeeded")
	}
}

func TestDecodeRequiresOneStrictCompleteDocument(t *testing.T) {
	t.Parallel()

	document := `{
  "version": 1,
  "revision": 7,
  "continue_existing": true,
  "screen_enabled": false,
  "card_detail": "standard",
  "show_technical_actions": true,
  "notify_background_questions": true,
  "notify_background_errors": true,
  "session_lifetime": "never",
  "queue_limit": 32,
  "voice_recognition": "parakeet",
  "retry_undelivered_files": false
}`

	snapshot, err := Decode(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if snapshot.Revision != 7 || snapshot.Settings != Default() {
		t.Fatalf("Decode() = %#v, want revision 7 and product defaults", snapshot)
	}

	invalid := []struct {
		name     string
		document string
	}{
		{name: "missing boolean", document: strings.Replace(document, "  \"screen_enabled\": false,\n", "", 1)},
		{name: "unknown", document: strings.Replace(document, `  "version": 1,`, `  "version": 1, "model": "gpt",`, 1)},
		{name: "duplicate", document: strings.Replace(document, `  "queue_limit": 32,`, `  "queue_limit": 32, "queue_limit": 64,`, 1)},
		{name: "trailing", document: document + `{}`},
		{name: "zero revision", document: strings.Replace(document, `"revision": 7`, `"revision": 0`, 1)},
	}
	for _, test := range invalid {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Decode(strings.NewReader(test.document)); err == nil {
				t.Fatal("Decode() error = nil, want strict rejection")
			}
		})
	}
}

func TestDecodeRejectsOversizedDocument(t *testing.T) {
	t.Parallel()
	if _, err := Decode(bytes.NewReader(bytes.Repeat([]byte{' '}, MaxDocumentBytes+1))); err == nil {
		t.Fatal("Decode() error = nil, want bounded-read rejection")
	}
}

func TestFileStoreReloadAppliesValidLocalEditAndRetainsLastValidOnInvalidEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(s *Settings) error {
		s.QueueLimit = 41
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	validEdit := bytes.Replace(data, []byte(`"queue_limit": 41`), []byte(`"queue_limit": 42`), 1)
	if err := os.WriteFile(path, validEdit, 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := store.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload(valid edit) error = %v", err)
	}
	if after.Revision != before.Revision+1 || after.Settings.QueueLimit != 42 {
		t.Fatalf("Reload(valid edit) = %#v, want incremented revision and queue limit 42", after)
	}

	if err := os.WriteFile(path, []byte(`{"version":1,"revision":999,"queue_limit":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reload(context.Background()); err == nil {
		t.Fatal("Reload(invalid edit) error = nil, want whole edit rejected")
	}
	active, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() after invalid edit error = %v, want active state retained", err)
	}
	if active.QueueLimit != 42 {
		t.Fatalf("Load() after invalid edit = %#v, want last valid state", active)
	}
	if store.LastReloadError() == nil {
		t.Fatal("LastReloadError() = nil, want invalid local edit observable")
	}
}

func TestFileStoreCASPersistsRevisionAndRejectsStaleWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next := initial.Settings
	next.ScreenEnabled = true
	committed, err := store.CompareAndSwap(context.Background(), initial.Revision, next)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Revision != initial.Revision+1 || !committed.Settings.ScreenEnabled {
		t.Fatalf("CompareAndSwap() = %#v", committed)
	}
	if _, err := store.CompareAndSwap(context.Background(), initial.Revision, Default()); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale CompareAndSwap() error = %v, want ErrRevisionConflict", err)
	}
	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reread, err := reopened.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reread != committed {
		t.Fatalf("reopened snapshot = %#v, want %#v", reread, committed)
	}
}

func TestEffectiveExposesCompositionContract(t *testing.T) {
	effective := Default().Effective()
	if effective.QueueLimit != DefaultQueueLimit || effective.SessionLifetime != LifetimeNever ||
		effective.ScreenEnabled || effective.CardDetail != CardDetailStandard ||
		!effective.NotifyBackgroundQuestions || !effective.NotifyBackgroundErrors ||
		!effective.NotifyBackgroundCompletion || !effective.ShowTechnicalActions {
		t.Fatalf("Effective() = %#v, want all contract defaults", effective)
	}
}

func TestOpenFileStoreRejectsUnsafeOrOversizedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file safety semantics are unavailable on Windows")
	}
	directory := t.TempDir()
	validPath := filepath.Join(directory, "valid.json")
	store, err := OpenFileStore(validPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(*Settings) error { return nil }); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(directory, "settings-link.json")
	if err := os.Symlink(validPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStore(symlinkPath); err == nil {
		t.Fatal("OpenFileStore(symlink) error = nil")
	}
	unsafePath := filepath.Join(directory, "unsafe.json")
	data, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unsafePath, data, 0o622); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafePath, 0o622); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStore(unsafePath); err == nil {
		t.Fatal("OpenFileStore(group-writable) error = nil")
	}
	oversizedPath := filepath.Join(directory, "oversized.json")
	if err := os.WriteFile(oversizedPath, bytes.Repeat([]byte{' '}, MaxDocumentBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStore(oversizedPath); err == nil {
		t.Fatal("OpenFileStore(oversized) error = nil")
	}
}

func TestMemoryStoreCASAllowsExactlyOneConcurrentWriter(t *testing.T) {
	store := NewMemoryStore()
	initial, err := store.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var successes int
	var mu sync.Mutex
	var wait sync.WaitGroup
	for index := 0; index < 16; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			next := initial.Settings
			next.QueueLimit += index + 1
			_, err := store.CompareAndSwap(context.Background(), initial.Revision, next)
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
				return
			}
			if !errors.Is(err, ErrRevisionConflict) {
				t.Errorf("CompareAndSwap() error = %v", err)
			}
		}()
	}
	wait.Wait()
	if successes != 1 {
		t.Fatalf("successful concurrent writers = %d, want 1", successes)
	}
}
