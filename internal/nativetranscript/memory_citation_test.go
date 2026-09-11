package nativetranscript

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const nativeMemoryCitation = `<oai-mem-citation>
<citation_entries>
MEMORY.md:12-14|note=[session context]
</citation_entries>
<rollout_ids>
</rollout_ids>
</oai-mem-citation>`

func TestCodexFinalDropsOnlyTerminalMemoryCitation(t *testing.T) {
	finalText := "Готово.\n\n" + nativeMemoryCitation
	encoded, err := json.Marshal(finalText)
	if err != nil {
		t.Fatal(err)
	}
	opts, _ := fixture(t, "codex", codexMeta()+`{"type":"event_msg","payload":{"type":"agent_message","phase":"final","message":`+string(encoded)+`}}`+"\n")
	reader, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	events, err := reader.Poll(context.Background())
	if err != nil || len(events) != 1 || events[0].Kind != KindFinal || events[0].Text != "Готово." {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestCodexResponseItemFinalDropsTerminalMemoryCitation(t *testing.T) {
	finalText := "Response item answer.\n\n" + nativeMemoryCitation
	encoded, _ := json.Marshal([]map[string]string{{"type": "output_text", "text": finalText}})
	body := codexMeta() + `{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":` + string(encoded) + `}}` + "\n"
	opts, _ := fixture(t, "codex", body)
	reader, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	events, err := reader.Poll(context.Background())
	if err != nil || len(events) != 1 || events[0].Kind != KindFinal || events[0].Text != "Response item answer." {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestCodexToolOutputAndMalformedFinalKeepMemoryText(t *testing.T) {
	malformed := "Не менять MEMORY.md\n\n" + strings.Replace(nativeMemoryCitation, "</rollout_ids>", "", 1)
	toolText := "tool read MEMORY.md\n" + nativeMemoryCitation
	malformedJSON, _ := json.Marshal(malformed)
	toolJSON, _ := json.Marshal(toolText)
	body := codexMeta() +
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":` + string(toolJSON) + `}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"agent_message","phase":"final","message":` + string(malformedJSON) + `}}` + "\n"
	opts, _ := fixture(t, "codex", body)
	reader, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	events, err := reader.Poll(context.Background())
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if events[0].Kind != KindTool || events[0].Metadata == nil || !toolTextEquals(events[0].Metadata.Result, toolText) {
		t.Fatalf("tool event changed: %#v", events[0])
	}
	if events[1].Kind != KindFinal || events[1].Text != malformed {
		t.Fatalf("malformed final changed: %#v", events[1])
	}
}

func TestClaudeFinalDropsTerminalMemoryCitation(t *testing.T) {
	textJSON, _ := json.Marshal("Done\n\n" + nativeMemoryCitation)
	body := `{"type":"user","sessionId":"` + testID + `","cwd":"/work","uuid":"turn-1","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n" +
		`{"type":"assistant","sessionId":"` + testID + `","cwd":"/work","message":{"stop_reason":"end_turn","content":[{"type":"text","text":` + string(textJSON) + `}]}}` + "\n"
	opts, _ := fixture(t, "claude", body)
	reader, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	events, err := reader.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[1].Kind != KindFinal || events[1].Text != "Done" {
		t.Fatalf("events=%#v", events)
	}
}
