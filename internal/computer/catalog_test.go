package computer_test

import (
	"errors"
	"testing"

	"bria/internal/computer"
	"bria/internal/domain"
)

func TestCatalogRoundTripPreservesSafeComputerDescription(t *testing.T) {
	catalog, err := computer.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	record := computer.Record{
		ID:              domain.ComputerID("workstation-1"),
		Name:            "Workstation",
		Fingerprint:     "sha256:1234",
		Status:          computer.StatusOnline,
		ProtocolVersion: 1,
		Capabilities: []computer.Capability{
			{Provider: domain.ProviderCodex, Enabled: true},
			{Provider: domain.ProviderClaude, Enabled: false},
		},
	}
	if err := catalog.Upsert(record); err != nil {
		t.Fatal(err)
	}

	restored, err := computer.RestoreCatalog(catalog.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	got, ok := restored.Lookup(record.ID)
	if !ok {
		t.Fatal("restored computer is missing")
	}
	if got.Name != record.Name || got.Fingerprint != record.Fingerprint || got.Status != record.Status || got.ProtocolVersion != record.ProtocolVersion {
		t.Fatalf("restored record = %#v, want %#v", got, record)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != record.Capabilities[0] || got.Capabilities[1] != record.Capabilities[1] {
		t.Fatalf("restored capabilities = %#v, want %#v", got.Capabilities, record.Capabilities)
	}
}

func TestCatalogRejectsDuplicateProviderCapability(t *testing.T) {
	catalog, err := computer.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	err = catalog.Upsert(computer.Record{
		ID:              "computer-1",
		Name:            "Computer",
		Fingerprint:     "fingerprint",
		Status:          computer.StatusOnline,
		ProtocolVersion: 1,
		Capabilities: []computer.Capability{
			{Provider: domain.ProviderCodex, Enabled: true},
			{Provider: domain.ProviderCodex, Enabled: false},
		},
	})
	if !errors.Is(err, computer.ErrInvalidRecord) {
		t.Fatalf("Upsert error = %v, want ErrInvalidRecord", err)
	}
}
