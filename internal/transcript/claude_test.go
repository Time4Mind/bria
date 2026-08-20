package transcript

import (
	"context"
	"path/filepath"
	"testing"
)

func TestReaderParsesClaudeEventsAndIgnoresImages(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/srv/acme_project"
	sessionID := "claude-session-1"
	path := filepath.Join(layout.claude, encodeClaudeWorkdir(workdir), sessionID+".jsonl")
	writeTestFile(t, path, `{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"content":[{"type":"text","text":"hello"}]}}
{"type":"assistant","timestamp":"2026-01-01T00:00:01Z","message":{"stop_reason":"tool_use","content":[{"type":"thinking","thinking":"inspect safely"},{"type":"text","text":"I will inspect it."},{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"README.md"}}]}}
{"type":"user","timestamp":"2026-01-01T00:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":true,"content":[{"type":"text","text":"<tool_use_error>denied</tool_use_error>"},{"type":"image","source":{"type":"base64","data":"aW1hZ2U="}}]}]}}
{"type":"assistant","timestamp":"2026-01-01T00:00:03Z","message":{"stop_reason":"end_turn","model":"claude-haiku-4-5","usage":{"input_tokens":1000,"cache_creation_input_tokens":2000,"cache_read_input_tokens":37000},"content":[{"type":"text","text":"Done."}]}}
`)

	events, err := newTestReader(t, layout, nil).Read(context.Background(), Request{
		Backend: BackendClaude, ProviderSessionID: sessionID, Workdir: workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("got %d events: %#v", len(events), events)
	}
	wantKinds := []EventKind{
		EventUserText, EventThinking, EventAssistantText,
		EventToolCall, EventToolResult, EventAssistantFinal,
	}
	for index, want := range wantKinds {
		if events[index].Kind != want {
			t.Errorf("event %d kind = %q, want %q", index, events[index].Kind, want)
		}
	}
	call := events[3]
	if call.ToolUseID != "tool-1" || call.ToolName != "Read" ||
		call.Head != "Read" || call.Body != `{"file_path":"README.md"}` {
		t.Errorf("unexpected tool call: %#v", call)
	}
	result := events[4]
	if result.ToolUseID != "tool-1" || result.Body != "denied" || !result.Error {
		t.Errorf("unexpected tool result: %#v", result)
	}
	if events[5].Text != "Done." || events[5].Timestamp != "2026-01-01T00:00:03Z" {
		t.Errorf("unexpected final event: %#v", events[5])
	}
	if events[5].ContextPercent == nil || *events[5].ContextPercent != 20 {
		t.Errorf("Claude context percent = %#v, want 20", events[5].ContextPercent)
	}
}

func TestClaudeStringContentAndEventLimit(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/tmp/project"
	sessionID := "session-2"
	path := filepath.Join(layout.claude, encodeClaudeWorkdir(workdir), sessionID+".jsonl")
	writeTestFile(t, path, `{"type":"user","message":{"content":"first"}}
{"type":"user","message":{"content":"second"}}
{"type":"assistant","message":{"stop_reason":"end_turn","content":"third"}}
`)
	reader := newTestReader(t, layout, func(config *Config) { config.MaxEvents = 2 })
	events, err := reader.Read(context.Background(), Request{
		Backend: BackendClaude, ProviderSessionID: sessionID, Workdir: workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Text != "second" || events[1].Text != "third" {
		t.Fatalf("unexpected bounded events: %#v", events)
	}
}

func TestClaudeSyntheticProviderErrorIsFinal(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/tmp/project"
	sessionID := "provider-error"
	path := filepath.Join(layout.claude, encodeClaudeWorkdir(workdir), sessionID+".jsonl")
	writeTestFile(t, path, `{"type":"user","timestamp":"2026-08-16T15:08:45Z","message":{"content":"hello"}}
{"type":"assistant","timestamp":"2026-08-16T15:08:46Z","error":"oauth_org_not_allowed","message":{"model":"<synthetic>","stop_reason":"stop_sequence","content":[{"type":"text","text":"Subscription access is disabled"}]}}
`)
	events, err := newTestReader(t, layout, nil).Read(context.Background(), Request{
		Backend: BackendClaude, ProviderSessionID: sessionID, Workdir: workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Kind != EventAssistantFinal ||
		!events[1].Error || events[1].Text != "Subscription access is disabled" {
		t.Fatalf("provider error was not emitted as a failed final: %#v", events)
	}
}

func TestClaudeLocalCommandProducesHiddenCompletionBoundary(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/tmp/project"
	sessionID := "local-command"
	path := filepath.Join(layout.claude, encodeClaudeWorkdir(workdir), sessionID+".jsonl")
	writeTestFile(t, path, `{"type":"user","timestamp":"2026-08-20T06:10:29Z","message":{"content":"<local-command-caveat>ignore this provider metadata</local-command-caveat>"}}
{"type":"user","timestamp":"2026-08-20T06:10:30Z","message":{"content":"<command-name>/model</command-name>\n<command-message>model</command-message>"}}
{"type":"user","timestamp":"2026-08-20T06:10:31Z","message":{"content":"<local-command-stdout>Set model to kimi</local-command-stdout>"}}
`)
	events, err := newTestReader(t, layout, nil).Read(context.Background(), Request{
		Backend: BackendClaude, ProviderSessionID: sessionID, Workdir: workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Kind != EventUserText || events[0].Text != "/model" ||
		events[1].Kind != EventAssistantFinal || !events[1].LocalCommand || events[1].Text != "" {
		t.Fatalf("local command events=%#v", events)
	}
	turn, ok := LatestCompletedTurn(events, BackendClaude)
	if !ok || !turn.Final.LocalCommand {
		t.Fatalf("local command turn=%#v ok=%v", turn, ok)
	}
}
