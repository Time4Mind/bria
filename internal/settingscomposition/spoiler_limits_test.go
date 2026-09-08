package settingscomposition_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/settingsport"
)

func TestIndependentSpoilerCyclesPersistAndProjectBothLimits(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p := settingscomposition.Preferences{Store: store}
	command, ok := any(p).(settingsport.TechnicalCommandPreferences)
	if !ok {
		t.Fatal("separate command-lines capability is not exposed")
	}
	output := settingsport.TechnicalOutputPreferences(p)
	before, err := p.Snapshot(ctx)
	if err != nil || before.TechnicalCommandLines != 10 || before.TechnicalOutputLines != 10 {
		t.Fatalf("initial projection command/output=%d/%d err=%v", before.TechnicalCommandLines, before.TechnicalOutputLines, err)
	}
	for _, axis := range []string{"command", "output"} {
		for _, want := range []int{20, 3, 5, 10} {
			if axis == "command" {
				err = command.CycleTechnicalCommandLines(ctx)
			} else {
				err = output.CycleTechnicalOutputLines(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := settings.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := reopened.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (settingscomposition.Preferences{Store: reopened}).Snapshot(ctx)
			wantCommand, wantOutput := 10, 10
			if axis == "command" {
				wantCommand = want
			} else {
				wantOutput = want
			}
			if err != nil || got.TechnicalCommandLines != wantCommand || got.TechnicalOutputLines != wantOutput || stored.TechnicalCommandLines != wantCommand || stored.TechnicalOutputLines != wantOutput || stored.Effective().TechnicalCommandLines != wantCommand || stored.Effective().TechnicalOutputLines != wantOutput {
				t.Fatalf("%s cycle lost independent durable/projection choice: got=%d/%d want=%d/%d err=%v", axis, got.TechnicalCommandLines, got.TechnicalOutputLines, wantCommand, wantOutput, err)
			}
		}
	}
	current, err := store.Current(ctx)
	if err != nil || current.Revision != 8 {
		t.Fatalf("cycles revision=%d err=%v", current.Revision, err)
	}
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(s *settings.Settings) error { s.TechnicalCommandLines = 40; return nil }); err == nil {
		t.Fatal("new command limit 40 was accepted")
	}
	if err := store.Update(ctx, func(s *settings.Settings) error { s.TechnicalOutputLines = 40; return nil }); err == nil {
		t.Fatal("new output limit 40 was accepted")
	}
	if afterBytes, err := os.ReadFile(path); err != nil || string(afterBytes) != string(beforeBytes) {
		t.Fatal("rejected update changed physical settings")
	}
}

func TestIndependentSpoilerConcurrentWritersDoNotLoseOtherAxis(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.json")
	one, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	two, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	command, output := settingscomposition.Preferences{Store: one}, settingscomposition.Preferences{Store: two}
	var writers sync.WaitGroup
	writers.Add(2)
	go func() {
		defer writers.Done()
		for range 5 {
			if err := command.CycleTechnicalCommandLines(ctx); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer writers.Done()
		for range 7 {
			if err := output.CycleTechnicalOutputLines(ctx); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	writers.Wait()
	reopened, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Current(ctx)
	if err != nil || got.Revision != 12 || got.Settings.TechnicalCommandLines != 20 || got.Settings.TechnicalOutputLines != 5 {
		t.Fatalf("concurrent updates lost independent choice: revision=%d command/output=%d/%d err=%v", got.Revision, got.Settings.TechnicalCommandLines, got.Settings.TechnicalOutputLines, err)
	}
}
