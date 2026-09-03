// Package computer contains transport- and storage-neutral rules for Bria
// computers and coordinator generations.
package computer

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"bria/internal/domain"
)

var ErrInvalidRecord = errors.New("invalid computer record")

// Status is the coordinator's safe, global view of a computer. It contains no
// provider credentials or local session history.
type Status string

const (
	StatusUnknown      Status = "unknown"
	StatusOnline       Status = "online"
	StatusOffline      Status = "offline"
	StatusIncompatible Status = "incompatible"
)

// Capability is the safe subset of one locally configured provider which an
// executor may advertise to the coordinator.
type Capability struct {
	Provider domain.Provider
	Enabled  bool
}

// Record is the durable-neutral catalog representation of one paired
// computer. Fingerprint is an identifier, not the computer's private key.
type Record struct {
	ID              domain.ComputerID
	Name            string
	Fingerprint     string
	Status          Status
	ProtocolVersion uint16
	Capabilities    []Capability
}

// CatalogSnapshot is safe to pass to a persistence adapter. Callers remain
// responsible for durable and atomic writes.
type CatalogSnapshot struct {
	Computers []Record
}

type Catalog struct {
	mu      sync.RWMutex
	records map[domain.ComputerID]Record
}

func NewCatalog() (*Catalog, error) {
	return &Catalog{records: make(map[domain.ComputerID]Record)}, nil
}

func RestoreCatalog(snapshot CatalogSnapshot) (*Catalog, error) {
	catalog, _ := NewCatalog()
	for _, record := range snapshot.Computers {
		if _, exists := catalog.records[record.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate computer %q", ErrInvalidRecord, record.ID)
		}
		if err := validateRecord(record); err != nil {
			return nil, err
		}
		catalog.records[record.ID] = cloneRecord(record)
	}
	return catalog, nil
}

func (catalog *Catalog) Upsert(record Record) error {
	if catalog == nil {
		return fmt.Errorf("%w: nil catalog", ErrInvalidRecord)
	}
	if err := validateRecord(record); err != nil {
		return err
	}
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	catalog.records[record.ID] = cloneRecord(record)
	return nil
}

func (catalog *Catalog) Lookup(id domain.ComputerID) (Record, bool) {
	if catalog == nil {
		return Record{}, false
	}
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	record, ok := catalog.records[id]
	return cloneRecord(record), ok
}

func (catalog *Catalog) Snapshot() CatalogSnapshot {
	if catalog == nil {
		return CatalogSnapshot{}
	}
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	ids := make([]string, 0, len(catalog.records))
	for id := range catalog.records {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	records := make([]Record, 0, len(ids))
	for _, id := range ids {
		records = append(records, cloneRecord(catalog.records[domain.ComputerID(id)]))
	}
	return CatalogSnapshot{Computers: records}
}

func validateRecord(record Record) error {
	if strings.TrimSpace(string(record.ID)) == "" {
		return fmt.Errorf("%w: computer id is required", ErrInvalidRecord)
	}
	if strings.TrimSpace(record.Name) == "" {
		return fmt.Errorf("%w: computer name is required", ErrInvalidRecord)
	}
	if strings.TrimSpace(record.Fingerprint) == "" {
		return fmt.Errorf("%w: computer fingerprint is required", ErrInvalidRecord)
	}
	switch record.Status {
	case StatusUnknown, StatusOnline, StatusOffline, StatusIncompatible:
	default:
		return fmt.Errorf("%w: unsupported status %q", ErrInvalidRecord, record.Status)
	}
	if record.ProtocolVersion == 0 {
		return fmt.Errorf("%w: protocol version is required", ErrInvalidRecord)
	}
	seen := make(map[domain.Provider]struct{}, len(record.Capabilities))
	for _, capability := range record.Capabilities {
		if capability.Provider != domain.ProviderCodex && capability.Provider != domain.ProviderClaude {
			return fmt.Errorf("%w: unsupported provider %q", ErrInvalidRecord, capability.Provider)
		}
		if _, duplicate := seen[capability.Provider]; duplicate {
			return fmt.Errorf("%w: duplicate provider %q", ErrInvalidRecord, capability.Provider)
		}
		seen[capability.Provider] = struct{}{}
	}
	return nil
}

func cloneRecord(record Record) Record {
	record.Capabilities = append([]Capability(nil), record.Capabilities...)
	return record
}
