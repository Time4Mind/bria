package settingscomposition_test

import (
	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"context"
	"testing"
)

func TestStandbyToggleIsIndependentAndVisibleInSnapshot(t *testing.T) {
	p := settingscomposition.Preferences{Store: settings.NewMemoryStore()}
	for _, want := range []bool{true, false} {
		if err := p.ToggleStandby(context.Background()); err != nil {
			t.Fatal(err)
		}
		snapshot, err := p.Snapshot(context.Background())
		if err != nil || snapshot.StandbyEnabled != want || !snapshot.ContinueExisting {
			t.Fatalf("snapshot=%#v err=%v", snapshot, err)
		}
	}
}
