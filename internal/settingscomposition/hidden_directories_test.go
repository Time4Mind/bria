package settingscomposition_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/settingsport"
)

func TestHiddenDirectoriesTogglePersistsAcrossReopenWithoutChangingOtherSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(s *settings.Settings) error {
		s.DefaultProviders["local"] = "claude"
		s.DefaultWorkdirs["local"] = "/workspace/.chosen"
		s.PreprocessingEnabled = true
		s.PreprocessingInstruction = "Сохрани команды."
		s.StandbyEnabled = true
		s.TechnicalCommandLines = 3
		s.TechnicalOutputLines = 20
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := settingscomposition.Preferences{Store: store}
	beforeView, err := p.Snapshot(ctx)
	if err != nil || beforeView.ShowHiddenDirectories {
		t.Fatalf("initial snapshot=%+v err=%v", beforeView, err)
	}
	for _, want := range []bool{true, false} {
		capability, ok := any(p).(settingsport.HiddenDirectoryPreferences)
		if !ok {
			t.Fatal("Preferences does not expose optional HiddenDirectoryPreferences")
		}
		if err := capability.ToggleHiddenDirectories(ctx); err != nil {
			t.Fatal(err)
		}
		reopened, err := settings.OpenFileStore(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := reopened.Load(ctx)
		wantSettings := before
		wantSettings.ShowHiddenDirectories = want
		if err != nil || !reflect.DeepEqual(got, wantSettings) || got.Effective().ShowHiddenDirectories != want {
			t.Fatalf("reopened settings=%+v err=%v, want %+v", got, err, wantSettings)
		}
		p = settingscomposition.Preferences{Store: reopened}
		view, err := p.Snapshot(ctx)
		wantView := beforeView
		wantView.ShowHiddenDirectories = want
		if err != nil || !reflect.DeepEqual(view, wantView) {
			t.Fatalf("reopened projection=%+v err=%v, want %+v", view, err, wantView)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		literal := []byte(`"show_hidden_directories": false`)
		if want {
			literal = []byte(`"show_hidden_directories": true`)
		}
		if !bytes.Contains(data, literal) {
			t.Fatal("physical settings document did not persist the preference")
		}
	}
}

func TestHiddenDirectoriesCanceledToggleDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p := settingscomposition.Preferences{Store: store}
	if err := p.ToggleHiddenDirectories(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.ToggleHiddenDirectories(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled toggle=%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("canceled toggle changed the physical document")
	}
	if err := (settingscomposition.Preferences{}).ToggleHiddenDirectories(context.Background()); err == nil {
		t.Fatal("missing store must reject toggle")
	}
}
