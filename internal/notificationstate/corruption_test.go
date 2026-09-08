package notificationstate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/notificationstate"
)

func TestDeletedV2PlanCannotRebindAsLegacy(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "retained empty"}[empty], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "parts.json")
			store := openStore(t, path)
			if _, err := store.GetOrCreatePlan(ctx, "op", hash, []string{"original"}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ClaimPart(ctx, "op", "op:part:1-of-1"); err != nil {
				t.Fatal(err)
			}
			if empty {
				if err := store.ResolveUnknownForRetry(ctx, "op", "op:part:1-of-1"); err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var state map[string]any
			if err := json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			delete(state["plans"].(map[string]any), "op")
			corrupt, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, corrupt, 0o600); err != nil {
				t.Fatal(err)
			}
			changedHash := strings.Repeat("b", 64)
			if _, err := store.GetOrCreatePlan(ctx, "op", changedHash, []string{"changed"}, []string{"changed legacy"}); err == nil {
				t.Error("deleted v2 plan rebound to changed input as legacy")
			}
			if _, _, err := store.LoadPlan(ctx, "op", changedHash); err == nil {
				t.Error("deleted v2 plan did not fail read closed")
			}
			if _, err := notificationstate.OpenFilePartReceiptStore(path); err == nil {
				t.Error("deleted v2 plan accepted on reopen")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(corrupt) {
				t.Error("rejected corruption rewrote file")
			}
		})
	}
}

func TestReadCorruptVersionTwoFailsClosedWithoutLeakingContent(t *testing.T) {
	const secret = "sensitive-payload-sentinel"
	for _, damage := range []string{"page", "hash", "checksum", "part", "version", "missingplans", "unknownfield", "duplicatekey", "invalidutf8", "missingversion", "statechecksum", "operation", "claim"} {
		t.Run(damage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "parts.json")
			store := openStore(t, path)
			if _, err := store.GetOrCreatePlan(ctx, "op", hash, []string{secret}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ClaimPart(ctx, "op", "op:part:1-of-1"); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var state map[string]any
			if err := json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			plan := state["plans"].(map[string]any)["op"].(map[string]any)
			switch damage {
			case "page":
				plan["pages"] = []string{"changed body"}
			case "hash":
				plan["input_hash"] = strings.Repeat("b", 64)
			case "checksum":
				delete(plan, "checksum")
			case "statechecksum":
				delete(state, "checksum")
			case "operation":
				delete(state["operations"].(map[string]any), "op")
			case "claim":
				delete(state["operations"].(map[string]any)["op"].(map[string]any), "op:part:1-of-1")
			case "part":
				state["operations"] = map[string]any{"op": map[string]any{"op:part:1-of-2": map[string]any{"state": "unknown"}}}
			case "version":
				state["version"] = 3
			case "missingplans":
				delete(state, "plans")
			case "unknownfield":
				state["secret"] = secret
			case "missingversion":
				state = map[string]any{"operations": map[string]any{}}
			}
			data, err = json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if damage == "duplicatekey" {
				data = []byte(strings.Replace(string(data), `"version":2`, `"version":1,"version":2`, 1))
			}
			if damage == "invalidutf8" {
				data = []byte(strings.Replace(string(data), secret, "\xff", 1))
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.LoadPlan(ctx, "op", hash); err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("corrupt load did not fail safely: %v", err)
			}
			if claimed, err := store.ClaimPart(ctx, "op", "op:part:1-of-1"); err == nil || claimed {
				t.Fatal("corrupt store allowed claim")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(data) {
				t.Fatal("corruption read/claim modified evidence")
			}
		})
	}
}

func TestInvalidNewPlanNeverPersists(t *testing.T) {
	for _, pages := range [][]string{nil, {""}, {"\xff"}, {strings.Repeat("x", 4097)}, {strings.Repeat("я", 2049)}} {
		path := filepath.Join(t.TempDir(), "parts.json")
		store := openStore(t, path)
		if _, err := store.GetOrCreatePlan(ctx, "op", hash, pages, nil); err == nil {
			t.Fatal("invalid new payload accepted")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("invalid plan created file: %v", err)
		}
	}
}

func TestReceiptBoundaryConflictCannotCorruptStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	store := openStore(t, path)
	if err := store.MarkPartUnknown(ctx, "op", "op:part:1-of-2"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPartUnknown(ctx, "op", "op:part:2-of-3"); err == nil {
		t.Fatal("mixed legacy totals accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("rejected receipt damaged durable file")
	}
	openStore(t, path)
}
