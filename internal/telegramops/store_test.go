package telegramops_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestFileStoreCompactsOnlyCommittedRecoveryPayloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	store, err := telegramops.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	committed := json.RawMessage(`{"id":"status:42","update_id":42,"phase":"committed","prepared":{"large":"payload"},"recovery":{"large":"payload"},"receipt":9}`)
	unknown := json.RawMessage(`{"id":"status:43","update_id":43,"phase":"effect_unknown","prepared":{"must":"remain"}}`)
	if err := store.Insert(context.Background(), telegramops.Callbacks, "status:42", committed); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(context.Background(), telegramops.Callbacks, "status:43", unknown); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(snapshot.Operations["status:42"], []byte(`"prepared"`)) {
		t.Fatalf("committed operation retained recovery payload: %s", snapshot.Operations["status:42"])
	}
	if !bytes.Contains(snapshot.Operations["status:42"], []byte(`"recovery"`)) {
		t.Fatalf("committed operation lost recovery identity: %s", snapshot.Operations["status:42"])
	}
	if !bytes.Contains(snapshot.Operations["status:43"], []byte(`"prepared"`)) {
		t.Fatalf("uncertain operation lost recovery payload: %s", snapshot.Operations["status:43"])
	}
}

func TestFileStoreBoundsFinalizedHistoryWithoutDroppingRecoveryState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	store, err := telegramops.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A coupled unknown status still needs its same-ID committed callback receipt
	// after restart. Active callback and acknowledgement records are never history.
	if err := store.Insert(ctx, telegramops.Callbacks, "status:1", json.RawMessage(`{"id":"status:1","update_id":1,"phase":"committed","receipt":11}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(ctx, telegramops.Statuses, "status:1", json.RawMessage(`{"id":"status:1","sequence":1,"phase":"send_unknown"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(ctx, telegramops.Callbacks, "status:2", json.RawMessage(`{"id":"status:2","update_id":2,"phase":"effect_unknown"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(ctx, telegramops.Acknowledgements, "status:3", json.RawMessage(`{"operation_id":"status:3","callback_query_id":"query-3","phase":"pending"}`)); err != nil {
		t.Fatal(err)
	}

	for sequence := 1000; sequence < 1300; sequence++ {
		id := fmt.Sprintf("status:%d", sequence)
		if err := store.Insert(ctx, telegramops.Callbacks, id, json.RawMessage(fmt.Sprintf(`{"id":%q,"update_id":%d,"phase":"committed","receipt":%d}`, id, sequence, sequence))); err != nil {
			t.Fatal(err)
		}
		if err := store.Insert(ctx, telegramops.Statuses, id, json.RawMessage(fmt.Sprintf(`{"id":%q,"sequence":%d,"phase":"committed","receipt":%d}`, id, sequence, sequence))); err != nil {
			t.Fatal(err)
		}
		if err := store.Insert(ctx, telegramops.Acknowledgements, id, json.RawMessage(fmt.Sprintf(`{"operation_id":%q,"callback_query_id":%q,"phase":"confirmed"}`, id, "query"))); err != nil {
			t.Fatal(err)
		}
	}

	reopened, err := telegramops.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Operations) > 258 || len(snapshot.Statuses) > 257 || len(snapshot.Acknowledgements) > 257 {
		t.Fatalf("unbounded finalized history: callbacks=%d statuses=%d acknowledgements=%d", len(snapshot.Operations), len(snapshot.Statuses), len(snapshot.Acknowledgements))
	}
	for namespace, id := range map[telegramops.Namespace]string{
		telegramops.Callbacks:        "status:2",
		telegramops.Statuses:         "status:1",
		telegramops.Acknowledgements: "status:3",
	} {
		if _, found, err := reopened.Load(ctx, namespace, id); err != nil || !found {
			t.Fatalf("active %s record %q = found %t, err %v", namespace, id, found, err)
		}
	}
	if _, found, err := reopened.Load(ctx, telegramops.Callbacks, "status:1"); err != nil || !found {
		t.Fatalf("coupled committed callback = found %t, err %v", found, err)
	}
	if _, found, err := reopened.Load(ctx, telegramops.Callbacks, "status:1000"); err != nil || found {
		t.Fatalf("old finalized callback = found %t, err %v", found, err)
	}
	if _, found, err := reopened.Load(ctx, telegramops.Callbacks, "status:1299"); err != nil || !found {
		t.Fatalf("recent finalized callback = found %t, err %v", found, err)
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
