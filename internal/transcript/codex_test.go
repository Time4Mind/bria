package transcript

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReaderParsesCodexEvents(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/srv/project"
	sessionID := "codex-session-1"
	path := filepath.Join(layout.codex, "2026", "08", "10", "rollout-a.jsonl")
	writeTestFile(t, path, `{"type":"session_meta","payload":{"id":"codex-session-1","cwd":"/srv/project"}}
{"timestamp":"t1","type":"event_msg","payload":{"type":"user_message","message":"question"}}
{"timestamp":"t2","type":"event_msg","payload":{"type":"agent_message","message":"working","phase":"commentary"}}
{"timestamp":"t3","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"private reasoning summary"}]}}
{"timestamp":"t4","type":"response_item","payload":{"type":"custom_tool_call","call_id":"call-1","name":"exec","input":"const x = 1;"}}
{"timestamp":"t5","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call-1","output":[{"type":"input_text","text":"ok"},{"type":"image","data":"ignored"}]}}
{"timestamp":"ignored","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"duplicate"}]}}
{"timestamp":"t6","type":"event_msg","payload":{"type":"agent_message","message":"answer","phase":"final_answer"}}
{"timestamp":"t7","ordinal":1,"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final","content":[{"type":"output_text","text":"new-format answer"}]}}
{"timestamp":"t8","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":47720},"model_context_window":258400}}}
{"timestamp":"t9","type":"event_msg","payload":{"type":"task_complete"}}
`)
	events, err := newTestReader(t, layout, nil).Read(context.Background(), Request{
		Backend: BackendCodex, ProviderSessionID: sessionID, Workdir: workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []EventKind{
		EventUserText, EventAssistantText, EventThinking, EventToolCall,
		EventToolResult, EventAssistantFinal, EventAssistantFinal, EventTurnComplete,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("got %d events: %#v", len(events), events)
	}
	for index, want := range wantKinds {
		if events[index].Kind != want {
			t.Errorf("event %d kind = %q, want %q", index, events[index].Kind, want)
		}
	}
	if events[2].Body != "private reasoning summary" {
		t.Errorf("unexpected reasoning: %#v", events[2])
	}
	if events[3].ToolUseID != "call-1" || events[3].ToolName != "exec" || events[3].Body != "const x = 1;" {
		t.Errorf("unexpected call: %#v", events[3])
	}
	if events[4].Body != "ok" {
		t.Errorf("unexpected result: %#v", events[4])
	}
	if events[len(events)-1].ContextPercent == nil || *events[len(events)-1].ContextPercent != 18 {
		t.Errorf("Codex context percent = %#v, want 18", events[len(events)-1].ContextPercent)
	}
}

func TestCodexTurnCompletesOnlyOnTaskComplete(t *testing.T) {
	finalAt := "2026-08-19T05:36:54.607Z"
	events := parseCodex([][]byte{
		[]byte(`{"timestamp":"` + finalAt + `","type":"event_msg","payload":{"type":"agent_message","message":"answer","phase":"final_answer"}}`),
	}, 1<<20)
	if _, ok := LatestCompletedTurn(events, BackendCodex); ok {
		t.Fatal("final_answer settled Codex before task_complete")
	}
	events = append(events, parseCodex([][]byte{
		[]byte(`{"timestamp":"2026-08-19T05:36:54.652Z","type":"event_msg","payload":{"type":"task_complete"}}`),
	}, 1<<20)...)
	turn, ok := LatestCompletedTurn(events, BackendCodex)
	if !ok || turn.Final.Text != "answer" {
		t.Fatalf("completed turn=%#v ok=%v", turn, ok)
	}
}

func TestReaderCachesOnlyUnchangedTranscriptVersion(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/srv/project"
	sessionID := "codex-cache-session"
	path := filepath.Join(layout.codex, "2026", "08", "17", "rollout-cache.jsonl")
	initial := `{"type":"session_meta","payload":{"id":"codex-cache-session","cwd":"/srv/project"}}
{"timestamp":"t1","type":"event_msg","payload":{"type":"user_message","message":"first"}}
`
	writeTestFile(t, path, initial)
	reader := newTestReader(t, layout, nil)
	request := Request{Backend: BackendCodex, ProviderSessionID: sessionID, Workdir: workdir}

	first, err := reader.Read(context.Background(), request)
	if err != nil || len(first) != 1 || first[0].Text != "first" {
		t.Fatalf("first read = %#v, %v", first, err)
	}
	first[0].Text = "mutated by caller"
	second, err := reader.Read(context.Background(), request)
	if err != nil || len(second) != 1 || second[0].Text != "first" {
		t.Fatalf("cached read = %#v, %v", second, err)
	}

	handle, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteString(
		`{"timestamp":"t2","type":"event_msg","payload":{"type":"agent_message","message":"second","phase":"final"}}` + "\n",
	); err != nil {
		handle.Close()
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := reader.Read(context.Background(), request)
	if err != nil || len(third) != 2 || third[1].Text != "second" {
		t.Fatalf("updated read = %#v, %v", third, err)
	}
}

func TestReaderAppendCacheKeepsFixedEventWindow(t *testing.T) {
	layout := newTestLayout(t)
	path := filepath.Join(layout.codex, "2026", "08", "17", "rollout-window.jsonl")
	writeTestFile(t, path, `{"type":"session_meta","payload":{"id":"codex-window","cwd":"/srv/project"}}
{"timestamp":"t1","type":"event_msg","payload":{"type":"user_message","message":"one"}}
{"timestamp":"t2","type":"event_msg","payload":{"type":"user_message","message":"two"}}
{"timestamp":"t3","type":"event_msg","payload":{"type":"user_message","message":"three"}}
`)
	reader := newTestReader(t, layout, func(config *Config) {
		config.MaxEvents = 3
		config.MaxReadBytes = 512
	})
	request := Request{Backend: BackendCodex, ProviderSessionID: "codex-window", Workdir: "/srv/project"}
	first, err := reader.Read(context.Background(), request)
	if err != nil || len(first) != 3 {
		t.Fatalf("initial window = %#v, %v", first, err)
	}
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := handle.WriteString(
		`{"timestamp":"t4","type":"event_msg","payload":{"type":"user_message","message":"four"}}` + "\n",
	)
	closeErr := handle.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append: write=%v close=%v", writeErr, closeErr)
	}
	second, err := reader.Read(context.Background(), request)
	if err != nil || len(second) != 3 || second[0].Text != "two" || second[2].Text != "four" {
		t.Fatalf("appended window = %#v, %v", second, err)
	}
}

func TestParseRecentEventsExpandsOnlyToLatestBoundedContext(t *testing.T) {
	lines := make([][]byte, 0, 900)
	for index := 0; index < 900; index++ {
		line := fmt.Sprintf(
			`{"timestamp":"t%d","type":"event_msg","payload":{"type":"user_message","message":"event-%d"}}`,
			index, index,
		)
		if index == 700 {
			line = `{"timestamp":"t700","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":500},"model_context_window":1000}}}`
		}
		lines = append(lines, []byte(line))
	}
	events, parsedLines := parseRecentEvents(BackendCodex, lines, 1<<20, 200)
	if parsedLines >= len(lines) {
		t.Fatalf("parsed all %d lines", parsedLines)
	}
	if len(events) != 200 || events[0].Text != "event-699" || events[199].Text != "event-899" {
		t.Fatalf("unexpected bounded events: first=%q last=%q count=%d",
			events[0].Text, events[len(events)-1].Text, len(events))
	}
	if events[len(events)-1].ContextPercent == nil ||
		*events[len(events)-1].ContextPercent != 50 {
		t.Fatalf("context percent = %#v", events[len(events)-1].ContextPercent)
	}
}

func TestReaderDefaultsToFourHundredEvents(t *testing.T) {
	layout := newTestLayout(t)
	reader := newTestReader(t, layout, nil)
	if reader.config.MaxEvents != 400 {
		t.Fatalf("default max events=%d, want 400", reader.config.MaxEvents)
	}
}

func TestTurnCompletionMarkersDoNotConsumeVisibleEventWindow(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"timestamp":"t1","type":"event_msg","payload":{"type":"user_message","message":"one"}}`),
		[]byte(`{"timestamp":"t2","type":"event_msg","payload":{"type":"agent_message","message":"answer","phase":"final"}}`),
		[]byte(`{"timestamp":"t3","type":"event_msg","payload":{"type":"task_complete"}}`),
		[]byte(`{"timestamp":"t4","type":"event_msg","payload":{"type":"user_message","message":"two"}}`),
		[]byte(`{"timestamp":"t5","type":"event_msg","payload":{"type":"user_message","message":"three"}}`),
	}
	events, _ := parseRecentEvents(BackendCodex, lines, 1<<20, 3)
	if visibleEventCount(events) != 3 || events[0].Kind != EventAssistantFinal ||
		events[len(events)-1].Text != "three" {
		t.Fatalf("completion marker changed visible window: %#v", events)
	}
}

func TestReaderFormatsCodexExecWrapperAsBash(t *testing.T) {
	layout := newTestLayout(t)
	path := filepath.Join(layout.codex, "2026", "08", "15", "rollout-exec.jsonl")
	writeTestFile(t, path, `{"type":"session_meta","payload":{"id":"codex-exec","cwd":"/srv/project"}}
{"timestamp":"t1","type":"response_item","payload":{"type":"custom_tool_call","call_id":"call-1","name":"exec","input":"const r = await tools.exec_command({\n  cmd: \"printf 'one\\ntwo\\n'\",\n  workdir: \"/srv/project\",\n  yield_time_ms: 10000\n});\ntext(r.output);\n"}}
`)
	events, err := newTestReader(t, layout, nil).Read(context.Background(), Request{
		Backend: BackendCodex, ProviderSessionID: "codex-exec", Workdir: "/srv/project",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events: %#v", len(events), events)
	}
	call := events[0]
	if call.Head != "Bash" || call.ToolName != "Bash" {
		t.Fatalf("tool identity was not normalized: %#v", call)
	}
	const want = "```bash\nprintf 'one\ntwo\n'\n```"
	if call.Body != want {
		t.Fatalf("body = %q, want %q", call.Body, want)
	}
	for _, hidden := range []string{"tools.exec_command", "workdir", "yield_time_ms", "text(r.output)"} {
		if strings.Contains(call.Body, hidden) {
			t.Errorf("service wrapper leaked %q into %q", hidden, call.Body)
		}
	}
}

func TestReaderHidesCodexInjectedUserContext(t *testing.T) {
	layout := newTestLayout(t)
	path := filepath.Join(layout.codex, "2026", "08", "13", "rollout-context.jsonl")
	writeTestFile(t, path, `{"type":"session_meta","payload":{"id":"codex-context","cwd":"/srv/project"}}
{"timestamp":"t1","ordinal":1,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /srv/project\n\n<INSTRUCTIONS>\nrules\n</INSTRUCTIONS>"},{"type":"input_text","text":"<environment_context>\n  <current_date>2026-08-13</current_date>\n</environment_context>"}]}}
{"timestamp":"t2","ordinal":2,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"actual request"},{"type":"input_text","text":"<environment_context>\n  <timezone>Europe/Moscow</timezone>\n</environment_context>"}]}}
{"timestamp":"t3","type":"event_msg","payload":{"type":"user_message","message":"<environment_context>\n  <current_date>2026-08-13</current_date>\n</environment_context>"}}
`)
	events, err := newTestReader(t, layout, nil).Read(context.Background(), Request{
		Backend: BackendCodex, ProviderSessionID: "codex-context", Workdir: "/srv/project",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != EventUserText || events[0].Text != "actual request" {
		t.Fatalf("service context leaked into events: %#v", events)
	}
}

func TestCodexResolverRequiresMatchingWorkdir(t *testing.T) {
	layout := newTestLayout(t)
	path := filepath.Join(layout.codex, "2026", "08", "10", "rollout-a.jsonl")
	writeTestFile(t, path, `{"type":"session_meta","payload":{"id":"codex-1","cwd":"/allowed"}}
`)
	_, err := newTestReader(t, layout, nil).Read(context.Background(), Request{
		Backend: BackendCodex, ProviderSessionID: "codex-1", Workdir: "/other",
	})
	if !errors.Is(err, ErrTranscriptNotFound) {
		t.Fatalf("got %v, want transcript not found", err)
	}
}
