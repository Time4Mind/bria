package computer_test

import (
	"path/filepath"
	"testing"

	"bria/internal/computer"
	"bria/internal/domain"
)

func TestCatalogAndFenceFileStoresRoundTripAfterReopen(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "catalog.json")
	fencePath := filepath.Join(dir, "fence.json")
	catalogStore, err := computer.OpenCatalogFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	wantCatalog := computer.CatalogSnapshot{Computers: []computer.Record{{
		ID: "executor-1", Name: "Laptop", Fingerprint: "sha256:executor", Status: computer.StatusOnline,
		ProtocolVersion: 1, Capabilities: []computer.Capability{{Provider: domain.ProviderCodex, Enabled: true}},
	}}}
	if err := catalogStore.Save(wantCatalog); err != nil {
		t.Fatal(err)
	}
	reopenedCatalog, err := computer.OpenCatalogFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	gotCatalog := reopenedCatalog.Snapshot()
	if len(gotCatalog.Computers) != 1 || gotCatalog.Computers[0].ID != "executor-1" || len(gotCatalog.Computers[0].Capabilities) != 1 {
		t.Fatalf("catalog snapshot = %#v", gotCatalog)
	}

	fenceStore, err := computer.OpenFenceFile(fencePath)
	if err != nil {
		t.Fatal(err)
	}
	wantFence := computer.FenceSnapshot{CoordinatorID: "coordinator-1", Generation: 4}
	if err := fenceStore.Save(wantFence); err != nil {
		t.Fatal(err)
	}
	reopenedFence, err := computer.OpenFenceFile(fencePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopenedFence.Snapshot(); got != wantFence {
		t.Fatalf("fence snapshot = %#v, want %#v", got, wantFence)
	}
}

func TestDurableCatalogAndFenceMutationsCommitBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "catalog.json")
	catalog, err := computer.OpenCatalogFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	record := computer.Record{ID: "executor-1", Name: "Laptop", Fingerprint: "sha256:executor", Status: computer.StatusOnline, ProtocolVersion: 1}
	if err := catalog.Upsert(record); err != nil {
		t.Fatal(err)
	}
	reopenedCatalog, err := computer.OpenCatalogFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reopenedCatalog.Lookup(record.ID); !ok || got.Name != record.Name {
		t.Fatalf("reopened Lookup = %#v, %v", got, ok)
	}

	fencePath := filepath.Join(dir, "fence.json")
	fence, err := computer.OpenFenceFile(fencePath)
	if err != nil {
		t.Fatal(err)
	}
	term := computer.CoordinatorTerm{CoordinatorID: "coordinator-1", Generation: 1}
	if err := fence.Accept(term); err != nil {
		t.Fatal(err)
	}
	reopenedFence, err := computer.OpenFenceFile(fencePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopenedFence.Validate(term); err != nil {
		t.Fatalf("reopened Validate error = %v", err)
	}
}
