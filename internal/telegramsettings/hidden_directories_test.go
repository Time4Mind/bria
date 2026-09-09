package telegramsettings_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/settingsport"
	"bria/internal/telegramsettings"
)

type hiddenDirectoryPreferences struct {
	settingsport.Preferences
	show bool
	err  error
}

func (p *hiddenDirectoryPreferences) ToggleHiddenDirectories(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.err != nil {
		return p.err
	}
	p.show = !p.show
	return nil
}

func TestHiddenDirectoriesApplyTogglesAndPreservesErrors(t *testing.T) {
	p := &hiddenDirectoryPreferences{}
	for _, want := range []bool{true, false} {
		if err := telegramsettings.Apply(context.Background(), p, nil, "settings_hidden_directories"); err != nil || p.show != want {
			t.Fatalf("show=%v error=%v, want %v", p.show, err, want)
		}
	}
	p.err = errors.New("save failed")
	if err := telegramsettings.Apply(context.Background(), p, nil, "settings_hidden_directories"); !errors.Is(err, p.err) || p.show {
		t.Fatalf("failed save: show=%v error=%v", p.show, err)
	}
	p.err = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := telegramsettings.Apply(ctx, p, nil, "settings_hidden_directories"); !errors.Is(err, context.Canceled) || p.show {
		t.Fatalf("canceled toggle: show=%v error=%v", p.show, err)
	}
}

func TestHiddenDirectoriesApplyRequiresOptionalCapability(t *testing.T) {
	for _, p := range []settingsport.Preferences{nil, struct{ settingsport.Preferences }{}} {
		if err := telegramsettings.Apply(context.Background(), p, nil, "settings_hidden_directories"); err == nil {
			t.Fatal("unavailable hidden-directory settings reported success")
		}
	}
}
