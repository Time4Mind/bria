package notificationstate_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"bria/internal/notificationstate"
)

var ctx = context.Background()
var hash = strings.Repeat("a", 64)

func openStore(t *testing.T, path string) *notificationstate.FilePartReceiptStore {
	t.Helper()
	store, err := notificationstate.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSavedPlanIsImmutableAndBoundAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	store := openStore(t, path)
	pages := []string{"Заголовок\n| a | b |\n|---|---|\n| 1 | 2 |", "```go\nvalue\n```"}
	want := append([]string(nil), pages...)
	plan, err := store.GetOrCreatePlan(ctx, "op", hash, pages, []string{"legacy"})
	if err != nil || plan.Version != 2 || !reflect.DeepEqual(plan.Pages, want) {
		t.Fatalf("create plan = %+v, %v", plan, err)
	}
	pages[0], plan.Pages[1] = "mutated caller", "mutated result"
	reopened := openStore(t, path)
	got, exists, err := reopened.LoadPlan(ctx, "op", hash)
	if err != nil || !exists || !reflect.DeepEqual(got.Pages, want) {
		t.Fatalf("saved immutable plan missing or changed: %+v, %v, %v", got, exists, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got.Pages[0] = "changed loaded page"
	got, err = reopened.GetOrCreatePlan(ctx, "op", hash, nil, nil)
	if err != nil || !reflect.DeepEqual(got.Pages, want) {
		t.Fatalf("saved plan recalculated: %+v, %v", got, err)
	}
	if _, _, err := reopened.LoadPlan(ctx, "op", strings.Repeat("b", 64)); err == nil {
		t.Fatal("LoadPlan accepted changed identity")
	}
	if _, err := reopened.GetOrCreatePlan(ctx, "op", strings.Repeat("b", 64), want, want); err == nil {
		t.Fatal("GetOrCreatePlan accepted changed identity")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("reads or rejected writes modified durable plan")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("plan file mode = %v, %v", info, err)
	}
}

func TestLegacyOperationsKeepPlanAndReceiptsIncludingEmptyMap(t *testing.T) {
	for _, parts := range []string{`{}`, `{"op:part:1-of-2":{"state":"confirmed","message_id":77},"op:part:2-of-2":{"state":"unknown"}}`} {
		t.Run(parts, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "parts.json")
			fixture := `{"version":1,"operations":{"op":` + parts + `}}`
			if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
				t.Fatal(err)
			}
			store := openStore(t, path)
			if _, exists, err := store.LoadPlan(ctx, "op", hash); err != nil || exists {
				t.Fatalf("v1 LoadPlan = %v, %v", exists, err)
			}
			unchanged, err := os.ReadFile(path)
			if err != nil || string(unchanged) != fixture {
				t.Fatal("opening/loading v1 rewrote file")
			}
			plan, err := store.GetOrCreatePlan(ctx, "op", hash, []string{"rich"}, []string{"old first", "old second"})
			if err != nil || plan.Version != 1 || !reflect.DeepEqual(plan.Pages, []string{"old first", "old second"}) {
				t.Fatalf("legacy boundaries changed: %+v, %v", plan, err)
			}
			reopened := openStore(t, path)
			if parts != `{}` {
				confirmed, err := reopened.ConfirmedParts(ctx, "op")
				if err != nil || len(confirmed) != 1 || confirmed[0].MessageID != 77 {
					t.Fatalf("legacy receipt changed: %+v, %v", confirmed, err)
				}
				unknown, err := reopened.UnknownParts(ctx, "op")
				if err != nil || !reflect.DeepEqual(unknown, []string{"op:part:2-of-2"}) {
					t.Fatalf("legacy unknown changed: %+v, %v", unknown, err)
				}
			}
		})
	}
}

func TestClaimIsExclusiveAndRetainedAfterCrashReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	store := openStore(t, path)
	if _, err := store.GetOrCreatePlan(ctx, "op", hash, []string{"page"}, nil); err != nil {
		t.Fatal(err)
	}
	var won atomic.Int64
	var workers sync.WaitGroup
	for range 20 {
		handle := openStore(t, path)
		workers.Add(1)
		go func() {
			defer workers.Done()
			claimed, err := handle.ClaimPart(ctx, "op", "op:part:1-of-1")
			if err != nil {
				t.Error(err)
			}
			if claimed {
				won.Add(1)
			}
		}()
	}
	workers.Wait()
	if won.Load() != 1 {
		t.Fatalf("concurrent claim winners = %d, want 1", won.Load())
	}
	reopened := openStore(t, path)
	claimed, err := reopened.ClaimPart(ctx, "op", "op:part:1-of-1")
	if err != nil || claimed {
		t.Fatalf("crashed claim retried = %v, %v", claimed, err)
	}
	unknown, err := reopened.UnknownParts(ctx, "op")
	if err != nil || !reflect.DeepEqual(unknown, []string{"op:part:1-of-1"}) {
		t.Fatalf("claim not persisted as unknown: %v, %v", unknown, err)
	}
	if err := reopened.ReleasePart(ctx, "op", "op:part:1-of-1"); err != nil {
		t.Fatal(err)
	}
	if claimed, err = reopened.ClaimPart(ctx, "op", "op:part:1-of-1"); err != nil || !claimed {
		t.Fatalf("explicitly released claim unavailable: %v, %v", claimed, err)
	}
	if err := reopened.ConfirmPart(ctx, "op", notificationstate.PartReceipt{PartID: "op:part:1-of-1", MessageID: 88}); err != nil {
		t.Fatal(err)
	}
	if claimed, err = reopened.ClaimPart(ctx, "op", "op:part:1-of-1"); err != nil || claimed {
		t.Fatalf("confirmed part claimed again: %v, %v", claimed, err)
	}
	if err := reopened.ReleasePart(ctx, "op", "op:part:1-of-1"); err == nil {
		t.Fatal("confirmed part released")
	}
}

func TestPlanRejectsLegacyBoundaryConflictWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	fixture := `{"version":1,"operations":{"op":{"op:part:1-of-3":{"state":"unknown"}}}}`
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openStore(t, path)
	if _, err := store.GetOrCreatePlan(ctx, "op", hash, []string{"rich"}, []string{"one", "two"}); err == nil {
		t.Fatal("legacy total conflicting with frozen pages accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != fixture {
		t.Fatal("failed upgrade changed original v1 fixture")
	}
}

func TestClaimRequiresValidSavedPlanBinding(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "parts.json"))
	if won, err := store.ClaimPart(ctx, "op", "op:part:1-of-1"); err == nil || won {
		t.Fatal("claimed part without durable plan")
	}
	if _, err := store.GetOrCreatePlan(ctx, "op", hash, []string{"one", "two"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"op:part:1-of-1", "op:part:3-of-2", "other:part:1-of-2"} {
		if won, err := store.ClaimPart(ctx, "op", part); err == nil || won {
			t.Errorf("unbound part claimed: %s", part)
		}
	}
}

func TestConcurrentPlanCreationKeepsSingleWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	var workers sync.WaitGroup
	results := make(chan string, 20)
	for n := range 20 {
		store := openStore(t, path)
		workers.Add(1)
		go func() {
			defer workers.Done()
			plan, err := store.GetOrCreatePlan(ctx, "op", hash, []string{fmt.Sprintf("page%d", n)}, nil)
			if err != nil {
				t.Error(err)
				return
			}
			results <- plan.Pages[0]
		}()
	}
	workers.Wait()
	close(results)
	unique := make(map[string]bool)
	for result := range results {
		unique[result] = true
	}
	if len(unique) != 1 {
		t.Fatalf("plan creation returned %d different plans", len(unique))
	}
}

func TestLegacyFinalPayloadMayExceedNewBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"operations":{"op":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("ю", 3000)
	store := openStore(t, path)
	plan, err := store.GetOrCreatePlan(ctx, "op", hash, nil, []string{want})
	if err != nil || plan.Version != 1 || !reflect.DeepEqual(plan.Pages, []string{want}) {
		t.Fatalf("legacy final payload rejected or changed: version=%d, err=%v", plan.Version, err)
	}
	reopened := openStore(t, path)
	plan, found, err := reopened.LoadPlan(ctx, "op", hash)
	if err != nil || !found || !reflect.DeepEqual(plan.Pages, []string{want}) {
		t.Fatal("legacy final payload changed after reopen")
	}
}

func TestResolvedLegacyEmptyOperationSurvivesOtherUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	store := openStore(t, path)
	if err := store.MarkPartUnknown(ctx, "old", "old:part:1-of-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveUnknownForRetry(ctx, "old", "old:part:1-of-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetOrCreatePlan(ctx, "new", hash, []string{"new rich"}, nil); err != nil {
		t.Fatal(err)
	}
	store = openStore(t, path)
	plan, err := store.GetOrCreatePlan(ctx, "old", hash, []string{"wrong"}, []string{"legacy"})
	if err != nil || plan.Version != 1 || !reflect.DeepEqual(plan.Pages, []string{"legacy"}) {
		t.Fatalf("empty retained legacy operation changed: %+v, %v", plan, err)
	}
}
