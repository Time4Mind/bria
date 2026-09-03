package telegramops_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"bria/internal/telegramops"
)

func TestFileStorePersistsIndependentNamespacesAndPhaseCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	store, err := telegramops.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	callback := json.RawMessage(`{"id":"callback","update_id":2,"phase":"effect_unknown"}`)
	status := json.RawMessage(`{"id":"status","sequence":1,"phase":"queued"}`)
	if err := store.Insert(context.Background(), telegramops.Callbacks, "callback", callback); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(context.Background(), telegramops.Statuses, "status", status); err != nil {
		t.Fatal(err)
	}
	next := json.RawMessage(`{"id":"status","sequence":1,"phase":"send_unknown"}`)
	if changed, err := store.CompareAndSwap(context.Background(), telegramops.Statuses, "status", "queued", next); err != nil || !changed {
		t.Fatalf("CAS = %t, %v", changed, err)
	}
	reopened, err := telegramops.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if records, err := reopened.List(context.Background(), telegramops.Callbacks, []string{"effect_unknown"}, 1); err != nil || len(records) != 1 {
		t.Fatalf("callback records = %q, %v", records, err)
	}
	if records, err := reopened.List(context.Background(), telegramops.Statuses, []string{"send_unknown"}, 1); err != nil || len(records) != 1 {
		t.Fatalf("status records = %q, %v", records, err)
	}
}

func TestStatusSequenceRequiresCanonicalPositiveStatusID(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		operationID string
		want        uint64
		valid       bool
	}{
		{operationID: "status:42", want: 42, valid: true},
		{operationID: "recovery:callback:42", want: 42, valid: true},
		{operationID: "callback:42"},
		{operationID: "status:0"},
		{operationID: "status:not-a-number"},
	} {
		got, err := telegramops.StatusSequence(test.operationID)
		if test.valid && (err != nil || got != test.want) {
			t.Fatalf("StatusSequence(%q) = %d, %v; want %d", test.operationID, got, err, test.want)
		}
		if !test.valid && err == nil {
			t.Fatalf("StatusSequence(%q) unexpectedly succeeded with %d", test.operationID, got)
		}
	}
}
