package recoveryruntime_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/recoveryruntime"
	"bria/internal/sessionruntime"
)

func TestNativeCorrelatedReceiptKeepsOutcomeWithoutTranscript(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	writeNative(t, root, `{"session_id":"`+nativeReceiptID+`","receipts":{"m":"unknown","done":"completed","bad":"failed"},"turn_ids":{"m":"turn-1","done":"turn-2"}}`)
	reader, err := recoveryruntime.NewNative(root, domain.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.ReadAcceptedTurns(context.Background(), nativeRequest(domain.ProviderCodex))
	if err != nil {
		t.Fatalf("valid atomic correlated receipt rejected: %v", err)
	}
	if len(got.Turns) != 3 || got.Turns[0].Outcome != sessionruntime.AcceptedTurnFailed || got.Turns[1].Outcome != sessionruntime.AcceptedTurnCompleted || got.Turns[2].Outcome != sessionruntime.AcceptedTurnUnknown {
		t.Fatalf("receipt outcomes changed: %#v", got)
	}
}

func TestNativeFinalProofPopulation(t *testing.T) {
	header := `{"type":"session_meta","payload":{"id":"` + nativeReceiptID + `","cwd":"/work"}}` + "\n"
	turn := `{"type":"turn_context","payload":{"turn_id":"turn-1"}}` + "\n"
	final := `{"type":"event_msg","payload":{"type":"agent_message","phase":"final_answer","message":"Recovered final"}}` + "\n"
	complete := `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1"}}` + "\n"
	for _, tc := range []struct{ name, mapping, body, saved, outcome, final string }{
		{"missing mapping", "", turn + final + complete, "unknown", "unknown", ""},
		{"missing turn", "turn-1", final + complete, "unknown", "unknown", ""},
		{"mismatched turn", "turn-2", turn + final + complete, "unknown", "unknown", ""},
		{"final without complete", "turn-1", turn + final, "unknown", "unknown", ""},
		{"partial complete", "turn-1", turn + final + strings.TrimSuffix(complete, "\n"), "unknown", "unknown", ""},
		{"partial final", "turn-1", turn + strings.TrimSuffix(final, "\n"), "unknown", "unknown", ""},
		{"partial trailing record", "turn-1", turn + final + complete + `{"type":"ignored"}`, "unknown", "unknown", ""},
		{"legacy completed partial trailing record", "turn-1", turn + final + complete + `{"type":"ignored"}`, "completed", "completed", ""},
		{"complete without final", "turn-1", turn + complete, "unknown", "unknown", ""},
		{"empty final", "turn-1", turn + strings.Replace(final, "Recovered final", "", 1) + complete, "unknown", "unknown", ""},
		{"final after complete", "turn-1", turn + complete + final, "unknown", "unknown", ""},
		{"commentary never final", "turn-1", turn + strings.Replace(final, "final_answer", "commentary", 1) + complete, "unknown", "unknown", ""},
		{"unknown text never matched", "", turn + `{"type":"event_msg","payload":{"type":"user_message","message":"m"}}` + "\n" + final + complete, "unknown", "unknown", ""},
		{"interrupted", "turn-1", turn + final + `{"type":"event_msg","payload":{"type":"turn_aborted","turn_id":"turn-1"}}` + "\n", "unknown", "failed", ""},
		{"saved failure remains", "turn-1", turn + final + complete, "failed", "failed", ""},
		{"saved completion remains", "turn-1", "", "completed", "completed", ""},
		{"malformed later record", "turn-1", turn + final + complete + "not json\n", "unknown", "unknown", ""},
		{"conflicting finals", "turn-1", turn + final + strings.Replace(final, "Recovered final", "Different final", 1) + complete, "unknown", "unknown", ""},
		{"legacy completed conflicting finals", "turn-1", turn + final + strings.Replace(final, "Recovered final", "Different final", 1) + complete, "completed", "completed", ""},
		{"exact final", "turn-1", turn + final + complete, "unknown", "completed", "Recovered final"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, transcript := t.TempDir(), t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			mapping := ""
			if tc.mapping != "" {
				mapping = `,"turn_ids":{"m":"` + tc.mapping + `"}`
			}
			writeNative(t, root, `{"session_id":"`+nativeReceiptID+`","receipts":{"m":"`+tc.saved+`"}`+mapping+`}`)
			if err := os.WriteFile(filepath.Join(transcript, "rollout-"+nativeReceiptID+".jsonl"), []byte(header+tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			reader, err := recoveryruntime.NewNativeWithTranscriptRoot(root, domain.ProviderCodex, transcript)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				got, err := reader.ReadAcceptedTurns(context.Background(), nativeRequest(domain.ProviderCodex))
				if err != nil || len(got.Turns) != 1 || string(got.Turns[0].Outcome) != tc.outcome || got.Turns[0].Final != tc.final {
					t.Fatalf("unproven result: %#v %v", got, err)
				}
			}
		})
	}
}

func TestNativeFinalRequiresExactCompletedTurnAndIsReadOnly(t *testing.T) {
	root, transcript := t.TempDir(), t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	receipt := `{"session_id":"` + nativeReceiptID + `","receipts":{"m":"unknown"},"turn_ids":{"m":"turn-1"}}`
	writeNative(t, root, receipt)
	path := filepath.Join(transcript, "rollout-"+nativeReceiptID+".jsonl")
	data := `{"type":"session_meta","payload":{"id":"` + nativeReceiptID + `","cwd":"/work"}}
{"type":"turn_context","payload":{"turn_id":"turn-1"}}
{"type":"event_msg","payload":{"type":"agent_message","phase":"final_answer","message":"Recovered final"}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1"}}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	receiptBefore, _ := os.Stat(filepath.Join(root, nativeReceiptID+".json"))
	reader, err := recoveryruntime.NewNativeWithTranscriptRoot(root, domain.ProviderCodex, transcript)
	if err != nil {
		t.Fatal(err)
	}
	want := []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnCompleted, TurnID: "turn-1", Final: "Recovered final"}}
	for i := 0; i < 2; i++ {
		got, err := reader.ReadAcceptedTurns(context.Background(), nativeRequest(domain.ProviderCodex))
		if err != nil || !reflect.DeepEqual(got.Turns, want) {
			t.Fatalf("exact completed final not restored: %#v %v", got, err)
		}
	}
	after, _ := os.Stat(path)
	receiptAfter, _ := os.Stat(filepath.Join(root, nativeReceiptID+".json"))
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || !os.SameFile(receiptBefore, receiptAfter) || !receiptBefore.ModTime().Equal(receiptAfter.ModTime()) {
		t.Fatal("read changed evidence")
	}
	for dir, expected := range map[string]string{root: receipt, transcript: data} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 {
			t.Fatalf("unexpected side effects: %v %v", entries, err)
		}
		got, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
		if err != nil || string(got) != expected {
			t.Fatal("evidence contents changed")
		}
	}
}

func TestNativeFinalRejectsForeignMissingAndOversizedEvidence(t *testing.T) {
	for _, scenario := range []string{"missing", "foreign session", "foreign workdir", "symlink", "oversized final"} {
		t.Run(scenario, func(t *testing.T) {
			root, transcript := t.TempDir(), t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			writeNative(t, root, `{"session_id":"`+nativeReceiptID+`","receipts":{"m":"unknown"},"turn_ids":{"m":"turn-1"}}`)
			id, cwd, final := nativeReceiptID, "/work", "Final"
			if scenario == "foreign session" {
				id = "00000000-0000-4000-8000-000000000099"
			}
			if scenario == "foreign workdir" {
				cwd = "/other"
			}
			if scenario == "oversized final" {
				final = strings.Repeat("x", (32<<10)+1)
			}
			path := filepath.Join(transcript, "rollout-"+nativeReceiptID+".jsonl")
			if scenario != "missing" {
				data := `{"type":"session_meta","payload":{"id":"` + id + `","cwd":"` + cwd + `"}}` + "\n" + `{"type":"turn_context","payload":{"turn_id":"turn-1"}}` + "\n" + `{"type":"event_msg","payload":{"type":"agent_message","phase":"final","message":"` + final + `"}}` + "\n" + `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1"}}` + "\n"
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				if scenario == "symlink" {
					if err := os.Rename(path, path+".actual"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(path+".actual", path); err != nil {
						t.Fatal(err)
					}
				}
			}
			reader, err := recoveryruntime.NewNativeWithTranscriptRoot(root, domain.ProviderCodex, transcript)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reader.ReadAcceptedTurns(context.Background(), nativeRequest(domain.ProviderCodex))
			want := sessionruntime.AcceptedTurnUnknown
			if err != nil || len(got.Turns) != 1 || got.Turns[0].Outcome != want || got.Turns[0].Final != "" {
				t.Fatalf("unsafe final published: %#v %v", got, err)
			}
		})
	}
}
