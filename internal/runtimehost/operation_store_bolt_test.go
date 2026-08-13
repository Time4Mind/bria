package runtimehost

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestBoltOperationStorePreservesPendingAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-operations.db")
	store, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.CreatePending("op-1", "fingerprint"); err != nil || !created {
		t.Fatalf("create pending: created=%v err=%v", created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, created, err := store.CreatePending("op-1", "fingerprint")
	if err != nil || created || record.State != OperationPending {
		t.Fatalf("reopened pending: record=%+v created=%v err=%v", record, created, err)
	}
	if _, _, err := store.CreatePending("op-1", "different"); !errors.Is(err, ErrOperationIDConflict) {
		t.Fatalf("fingerprint conflict = %v", err)
	}
}
