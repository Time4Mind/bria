package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStandbyDefaultsOffAndPersistsIndependently(t *testing.T) {
	if Default().StandbyEnabled || Default().Effective().StandbyEnabled {
		t.Fatal("standby enabled by default")
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(s *Settings) error { s.StandbyEnabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Load(context.Background())
	if err != nil || !got.StandbyEnabled || !got.ContinueExisting || !got.Effective().StandbyEnabled {
		t.Fatalf("settings=%#v err=%v", got, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "standby_enabled")
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := Decode(bytes.NewReader(data))
	if err != nil || legacy.Settings.StandbyEnabled {
		t.Fatalf("legacy=%#v err=%v", legacy, err)
	}
}
