package providerbinding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestStoreDeleteIsExactAndIdempotent(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	first := domain.SessionRef{NodeID: "node", SessionID: "first"}
	second := domain.SessionRef{NodeID: "node", SessionID: "second"}
	if err := store.Put(providerBindingRecordAt(first, 1, time.Unix(1, 0).UTC())); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(providerBindingRecordAt(second, 1, time.Unix(1, 0).UTC())); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(first); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LookupRef(first); err != nil || found {
		t.Fatalf("exact delete retained first binding: found=%v err=%v", found, err)
	}
	if _, found, err := store.LookupRef(second); err != nil || !found {
		t.Fatalf("exact delete removed second binding: found=%v err=%v", found, err)
	}
	if err := store.Delete(first); err != nil {
		t.Fatalf("repeated exact delete: %v", err)
	}
}

func TestStoreRejectsOlderGenerationAndConditionalDeleteIsSafe(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	newer := providerBindingRecordAt(ref, 2, time.Unix(20, 0).UTC())
	older := providerBindingRecordAt(ref, 1, time.Unix(10, 0).UTC())
	if err := store.Put(newer); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(older); err != nil {
		t.Fatal(err)
	}
	if record, found, err := store.LookupRef(ref); err != nil || !found || record.RuntimeGeneration != 2 {
		t.Fatalf("older hook replaced newer binding: record=%#v found=%v err=%v", record, found, err)
	}
	if err := store.DeleteIfGeneration(ref, 1); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LookupRef(ref); err != nil || !found {
		t.Fatalf("stale conditional delete removed newer binding: found=%v err=%v", found, err)
	}
	if err := store.DeleteIfGeneration(ref, 2); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LookupRef(ref); err != nil || found {
		t.Fatalf("matching conditional delete retained binding: found=%v err=%v", found, err)
	}
	if err := store.DeleteIfGeneration(ref, 2); err != nil {
		t.Fatalf("repeated conditional delete: %v", err)
	}
}

func TestStoreAcceptsLegacyUntilNewerGenerationExists(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	legacy := providerBindingRecordAt(ref, 0, time.Unix(10, 0).UTC())
	if err := store.Put(legacy); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIfGeneration(ref, 0); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LookupRef(ref); err != nil || found {
		t.Fatalf("legacy conditional delete failed: found=%v err=%v", found, err)
	}
	if err := store.Put(legacy); err != nil {
		t.Fatal(err)
	}
	newer := providerBindingRecordAt(ref, 1, time.Unix(20, 0).UTC())
	if err := store.Put(newer); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(legacy); err != nil {
		t.Fatal(err)
	}
	if record, found, err := store.LookupRef(ref); err != nil || !found || record.RuntimeGeneration != 1 {
		t.Fatalf("legacy hook overwrote newer binding: record=%#v found=%v err=%v", record, found, err)
	}
}

func TestStoreSweepRequiresArchivedTargetAbsenceAndHonorsMissingCutoff(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	live := domain.SessionRef{NodeID: "node", SessionID: "live"}
	archived := domain.SessionRef{NodeID: "node", SessionID: "archived"}
	missing := domain.SessionRef{NodeID: "node", SessionID: "missing"}
	for _, record := range []Record{
		providerBindingRecordAt(live, 1, time.Unix(100, 0).UTC()),
		providerBindingRecordAt(archived, 2, time.Unix(100, 0).UTC()),
		providerBindingRecordAt(missing, 3, time.Unix(100, 0).UTC()),
	} {
		if err := store.Put(record); err != nil {
			t.Fatal(err)
		}
	}

	input := SweepInput{
		KeepRefs: []domain.SessionRef{live},
		Archived: []SweepArchived{{Ref: archived, RuntimeGeneration: 2, TargetAbsent: false}},
	}
	if err := store.Sweep(input); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LookupRef(live); err != nil || !found {
		t.Fatalf("live binding was swept: found=%v err=%v", found, err)
	}
	if _, found, err := store.LookupRef(archived); err != nil || !found {
		t.Fatalf("archived target was swept without caller confirmation: found=%v err=%v", found, err)
	}
	if _, found, err := store.LookupRef(missing); err != nil || !found {
		t.Fatalf("missing binding was swept too early: found=%v err=%v", found, err)
	}

	input.Archived[0].TargetAbsent = true
	input.MissingBefore = time.Time{}
	if err := store.Sweep(input); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LookupRef(archived); err != nil || found {
		t.Fatalf("confirmed archived binding remained: found=%v err=%v", found, err)
	}
	if _, found, err := store.LookupRef(live); err != nil || !found {
		t.Fatalf("live binding changed during archived sweep: found=%v err=%v", found, err)
	}
	if _, found, err := store.LookupRef(missing); err != nil || !found {
		t.Fatalf("unlisted binding was swept without missing cutoff: found=%v err=%v", found, err)
	}

	if err := store.Sweep(SweepInput{KeepRefs: []domain.SessionRef{live}, MissingBefore: time.Unix(200, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LookupRef(missing); err != nil || found {
		t.Fatalf("missing binding survived caller cutoff: found=%v err=%v", found, err)
	}
}

func TestStoreRejectsOversizedEncodedOutputWithoutReplacingStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	if err := store.Put(providerBindingRecordAt(ref, 1, time.Unix(1, 0).UTC())); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oversized := providerBindingRecordAt(ref, 1, time.Unix(2, 0).UTC())
	oversized.ProviderSessionID = strings.Repeat("x", maxStoreBytes)
	if err := store.Put(oversized); err == nil {
		t.Fatal("oversized encoded store was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("oversized Put replaced existing store")
	}
	var records map[string]Record
	if err := json.Unmarshal(after, &records); err != nil {
		t.Fatal(err)
	}
}

func providerBindingRecordAt(ref domain.SessionRef, generation uint64, updatedAt time.Time) Record {
	return Record{
		NodeID: string(ref.NodeID), SessionID: string(ref.SessionID),
		ProviderSessionID: "provider-session-019fffe8-02ee-7aa1-b6cf-eed13a005482",
		Workdir:           "/tmp/bria-workdir", TmuxSession: "bria", TmuxWindow: "window",
		RuntimeGeneration: generation, UpdatedAt: updatedAt,
	}
}
