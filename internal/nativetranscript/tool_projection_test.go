package nativetranscript

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"bria/internal/tooltext"
)

func TestOversizedToolResultPreservesBindingAndFollowingTerminal(t *testing.T) {
	large := strings.Repeat("private-discarded-output", 100000)
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			header := codexMeta() + `{"type":"turn_context","payload":{"turn_id":"turn-live"}}` + "\n"
			record := `{"type":"response_item","payload":{"type":"function_call_output","call_id":"tool-exact","output":"` + large + `","status":"completed","internal_chat_message_metadata_passthrough":{"turn_id":"turn-live"}}}` + "\n"
			terminal := `{"type":"event_msg","payload":{"type":"agent_message","phase":"final","turn_id":"turn-live","message":"exact final"}}` + "\n" +
				`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-live"}}` + "\n"
			if provider == "claude" {
				header = `{"type":"user","sessionId":"` + testID + `","cwd":"/work","uuid":"turn-live","message":{"content":"accepted"}}` + "\n"
				record = `{"type":"user","sessionId":"` + testID + `","cwd":"/work","message":{"content":[{"type":"tool_result","tool_use_id":"tool-exact","content":"` + large + `","is_error":true}]}}` + "\n"
				terminal = `{"type":"assistant","sessionId":"` + testID + `","message":{"content":[{"type":"text","text":"exact final"}],"stop_reason":"end_turn"}}` + "\n"
			}
			opts, path := fixture(t, provider, header+record)
			r, err := Open(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			events, err := r.Poll(context.Background())
			if err != nil {
				t.Fatalf("oversized tool output terminated observation: %v", err)
			}
			tools := 0
			for _, event := range events {
				if event.Kind == KindComplete || event.Kind == KindFinal {
					t.Fatal("tool projection invented terminal outcome")
				}
				if event.Kind != KindTool {
					continue
				}
				tools++
				if event.SessionID != testID || event.TurnID != "turn-live" || event.ID != strconv.Itoa(len(header))+":0" || event.Metadata == nil || event.Metadata.ItemID != "tool-exact" {
					t.Fatalf("projected tool identity changed: %+v", event)
				}
				text, truncated := tooltext.Read(event.Metadata.Result)
				if !truncated || text == "" || len(text) > 512 || strings.Contains(text, "private-discarded-output") {
					t.Fatalf("missing bounded diagnostic projection: bytes=%d truncated=%t", len(text), truncated)
				}
				wantStatus := "completed"
				if provider == "claude" {
					wantStatus = "failed"
				}
				if event.Metadata.Status != wantStatus {
					t.Fatalf("tool status changed: %q", event.Metadata.Status)
				}
			}
			if tools != 1 {
				t.Fatalf("tool projection count=%d", tools)
			}
			appendFile(t, path, terminal)
			events, err = r.Poll(context.Background())
			if err != nil || len(events) != 2 || events[0].Kind != KindFinal || events[0].Text != "exact final" || events[1].Kind != KindComplete {
				t.Fatalf("following terminal lost: %+v %v", events, err)
			}
			for _, event := range events {
				if event.SessionID != testID || event.TurnID != "turn-live" {
					t.Fatal("terminal binding lost")
				}
			}
			if again, err := r.Poll(context.Background()); err != nil || len(again) != 0 {
				t.Fatal("projected tool or terminal replayed")
			}
		})
	}
}
