package nativetranscript

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func compactedHistoryRecord(t *testing.T) string {
	t.Helper()
	var replacement, guardian []any
	for i := 0; i < 7; i++ {
		replacement = append(replacement, map[string]any{"type": "message", "role": "assistant", "content": strings.Repeat("x", 100000)})
	}
	for i := 0; i < 130; i++ {
		guardian = append(guardian, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "agent_message", "phase": "final", "message": strings.Repeat("y", 4000)}})
	}
	record := map[string]any{
		"timestamp": "2026-09-09T07:08:52.377Z", "ordinal": 123, "type": "compacted",
		"payload": map[string]any{
			"message": "", "replacement_history": replacement, "guardian_history": guardian,
			"window_number": 2, "first_window_id": testID, "previous_window_id": testID, "window_id": testID,
			"compaction_response_id":    strings.Repeat("r", 55),
			"latest_token_usage_record": map[string]int{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 7, "h": 8},
		},
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	const observedBytes = 1360107 // Actual record size, excluding its newline.
	if len(data) >= observedBytes {
		t.Fatal("compacted fixture exceeds the observed record size")
	}
	replacement[0].(map[string]any)["content"] = strings.Repeat("x", 100000+observedBytes-len(data))
	data, err = json.Marshal(record)
	if err != nil || len(data) != observedBytes {
		t.Fatal("compacted fixture does not match observed size")
	}
	return string(data) + "\n"
}

func TestReaderCompactedHistoryPreservesAdjacentFinalsAndOffsets(t *testing.T) {
	large := compactedHistoryRecord(t)
	before := `{"type":"event_msg","payload":{"type":"agent_message","phase":"final","turn_id":"before-turn","message":"before"}}` + "\n"
	after := `{"type":"event_msg","payload":{"type":"agent_message","phase":"final","turn_id":"after-turn","message":"После compaction 😀"}}` + "\n"
	complete := `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"after-turn"}}` + "\n"
	for _, scan := range []bool{false, true} {
		t.Run(strconv.FormatBool(scan), func(t *testing.T) {
			opts, _ := fixture(t, "codex", codexMeta()+before+large+after+complete)
			r, err := Open(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			var events []Event
			if scan {
				err = r.Scan(context.Background(), func(batch []Event) error { events = append(events, batch...); return nil })
			} else {
				events, err = r.Poll(context.Background())
			}
			if err != nil {
				t.Fatalf("compacted history terminated reader: %v", err)
			}
			if len(events) != 3 || events[0].Kind != KindFinal || events[0].Text != "before" || events[1].Kind != KindFinal || events[1].Text != "После compaction 😀" || events[2].Kind != KindComplete {
				t.Fatal("compaction replayed history or lost adjacent final/completion")
			}
			for i, want := range []struct {
				offset int
				turn   string
			}{
				{len(codexMeta()), "before-turn"},
				{len(codexMeta()) + len(before) + len(large), "after-turn"},
				{len(codexMeta()) + len(before) + len(large) + len(after), "after-turn"},
			} {
				if events[i].ID != strconv.Itoa(want.offset)+":0" || events[i].TurnID != want.turn || events[i].SessionID != testID {
					t.Fatal("compaction changed physical offset or exact identity")
				}
			}
			if events, err := r.Poll(context.Background()); err != nil || len(events) != 0 {
				t.Fatal("compaction caused committed events to replay")
			}
		})
	}
}

func TestReaderOversizedCompactedDiscriminatorCannotBeSpoofed(t *testing.T) {
	large := compactedHistoryRecord(t)
	for _, test := range []struct {
		name, record string
		want         error
	}{
		{"nested type only", `{"type":"ignored","payload":` + strings.TrimSpace(large) + "}\n", ErrLimit},
		{"wrong top level", strings.Replace(large, `"type":"compacted"`, `"type":"unknown"`, 1), ErrLimit},
		{"duplicate top type", strings.TrimSuffix(large, "}\n") + `,"\u0074ype":"compacted"}` + "\n", ErrMalformed},
		{"duplicate history key", strings.Replace(large, `"role":"assistant"`, `"role":"assistant","\u0072ole":"user"`, 1), ErrMalformed},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts, _ := fixture(t, "codex", codexMeta()+test.record)
			r, err := Open(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			for retry := 0; retry < 2; retry++ {
				if events, err := r.Poll(context.Background()); len(events) != 0 || !errors.Is(err, test.want) {
					t.Fatalf("spoof accepted: count=%d err=%v", len(events), err)
				}
			}
		})
	}
}

func TestReaderCompactedPartialAndCanceledBatchDoNotLoseFinal(t *testing.T) {
	large := compactedHistoryRecord(t)
	before := `{"type":"event_msg","payload":{"type":"agent_message","phase":"final","turn_id":"turn","message":"before"}}` + "\n"
	after := `{"type":"event_msg","payload":{"type":"agent_message","phase":"final","turn_id":"turn","message":"after"}}` + "\n"
	for _, partial := range []bool{false, true} {
		t.Run(strconv.FormatBool(partial), func(t *testing.T) {
			body := codexMeta() + before + large + after
			cut := len(codexMeta()) + len(before) + 1100000
			initial := body
			if partial {
				initial = body[:cut]
			}
			opts, path := fixture(t, "codex", initial)
			r, err := Open(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			var got []Event
			if partial {
				got, err = r.Poll(context.Background())
				if err != nil || len(got) != 1 || got[0].Text != "before" {
					t.Fatal("partial compaction lost preceding final")
				}
				if extra, err := r.Poll(context.Background()); err != nil || len(extra) != 0 {
					t.Fatal("partial compaction emitted premature events")
				}
				appendFile(t, path, body[cut:])
			}
			remaining := cut + 1
			if partial {
				remaining = 100
			}
			for retry := 0; retry < 2; retry++ {
				ctx := &checkpointContext{Context: context.Background(), remaining: remaining}
				if events, err := r.Poll(ctx); !errors.Is(err, context.Canceled) || len(events) != 0 {
					t.Fatal("canceled compaction committed a partial batch")
				}
			}
			events, err := r.Poll(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, events...)
			if len(got) != 2 || got[0].Text != "before" || got[1].Text != "after" || got[1].ID != strconv.Itoa(len(codexMeta())+len(before)+len(large))+":0" {
				t.Fatal("retry lost, duplicated, or misidentified adjacent finals")
			}
			if replay, err := r.Poll(context.Background()); err != nil || len(replay) != 0 {
				t.Fatal("completed compaction replayed events")
			}
		})
	}
}
