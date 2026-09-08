package recoveryruntime_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/recoveryruntime"
)

func TestNativeTerminalFailureRequiresFreshExactUnambiguousProof(t *testing.T) {
	header := `{"type":"session_meta","payload":{"id":"` + nativeReceiptID + `","cwd":"/work"}}` + "\n"
	turn := `{"type":"turn_context","payload":{"turn_id":"turn-1"}}` + "\n"
	interrupted := `{"type":"event_msg","payload":{"type":"turn_aborted","turn_id":"turn-1"}}` + "\n"
	complete := `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1"}}` + "\n"
	final := `{"type":"event_msg","payload":{"type":"agent_message","phase":"final_answer","message":"synthetic final"}}` + "\n"
	for _, tc := range []struct {
		name, saved, mapping, body, outcome string
		proven                              bool
	}{
		{"unknown exact interrupted", "unknown", "turn-1", turn + interrupted, "failed", true},
		{"failed exact interrupted", "failed", "turn-1", turn + interrupted, "failed", true},
		{"interrupted after final", "unknown", "turn-1", turn + final + interrupted, "failed", true},
		{"duplicate same terminal", "unknown", "turn-1", turn + interrupted + interrupted, "failed", true},
		{"legacy failed alone", "failed", "", "", "failed", false},
		{"correlated failed alone", "failed", "turn-1", turn, "failed", false},
		{"unknown alone", "unknown", "turn-1", turn, "unknown", false},
		{"missing mapping", "unknown", "", turn + interrupted, "unknown", false},
		{"foreign turn", "unknown", "turn-2", turn + interrupted, "unknown", false},
		{"unbound event", "unknown", "turn-1", strings.ReplaceAll(interrupted, `,"turn_id":"turn-1"`, ""), "unknown", false},
		{"complete then interrupted", "unknown", "turn-1", turn + complete + interrupted, "unknown", false},
		{"interrupted then complete", "unknown", "turn-1", turn + interrupted + complete, "unknown", false},
		{"failed conflicting terminals", "failed", "turn-1", turn + complete + interrupted, "failed", false},
		{"saved completed conflict", "completed", "turn-1", turn + interrupted, "completed", false},
		{"ambiguous finals", "unknown", "turn-1", turn + final + strings.Replace(final, "synthetic final", "different final", 1) + interrupted, "unknown", false},
		{"partial terminal", "unknown", "turn-1", turn + strings.TrimSuffix(interrupted, "\n"), "unknown", false},
		{"partial tail", "unknown", "turn-1", turn + interrupted + `{"type":"ignored"}`, "unknown", false},
		{"malformed tail", "unknown", "turn-1", turn + interrupted + "not json\n", "unknown", false},
		{"failed malformed tail", "failed", "turn-1", turn + interrupted + "not json\n", "failed", false},
		{"record budget", "unknown", "turn-1", turn + interrupted + strings.Replace(final, "synthetic final", strings.Repeat("x", (1<<20)+1), 1), "unknown", false},
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
			receipt := `{"session_id":"` + nativeReceiptID + `","receipts":{"m":"` + tc.saved + `"}` + mapping + `}`
			writeNative(t, root, receipt)
			path := filepath.Join(transcript, "rollout-"+nativeReceiptID+".jsonl")
			data := header + tc.body
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			reader, err := recoveryruntime.NewNativeWithTranscriptRoot(root, domain.ProviderCodex, transcript)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				got, err := reader.ReadAcceptedTurns(context.Background(), nativeRequest(domain.ProviderCodex))
				if err != nil || len(got.Turns) != 1 {
					t.Fatalf("read failed: %v", err)
				}
				g := got.Turns[0]
				if string(g.Outcome) != tc.outcome || g.TerminalFailureProven != tc.proven || g.MessageID != "m" || g.TurnID != tc.mapping || g.Final != "" {
					t.Fatalf("outcome=%s failure_proven=%v want=%s/%v exact_identity=%v no_final=%v", g.Outcome, g.TerminalFailureProven, tc.outcome, tc.proven, g.MessageID == "m" && g.TurnID == tc.mapping, g.Final == "")
				}
			}
			for path, want := range map[string]string{path: data, filepath.Join(root, nativeReceiptID+".json"): receipt} {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatal("proof lookup changed physical evidence")
				}
			}
			// Reusing this reader must rescan; previously valid proof is not cached.
			if tc.proven {
				if err := os.WriteFile(path, []byte(data+complete), 0600); err != nil {
					t.Fatal(err)
				}
				got, err := reader.ReadAcceptedTurns(context.Background(), nativeRequest(domain.ProviderCodex))
				if err != nil || len(got.Turns) != 1 || got.Turns[0].TerminalFailureProven || string(got.Turns[0].Outcome) != tc.saved {
					t.Fatal("fresh conflicting evidence retained stale failure proof")
				}
			}
		})
	}
}
