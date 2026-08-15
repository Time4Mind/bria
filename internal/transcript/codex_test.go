package transcript

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
`)
	events, err := newTestReader(t, layout, nil).Read(context.Background(), Request{
		Backend: BackendCodex, ProviderSessionID: sessionID, Workdir: workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []EventKind{
		EventUserText, EventAssistantText, EventThinking, EventToolCall,
		EventToolResult, EventAssistantFinal, EventAssistantFinal,
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

func TestCodexResolveCacheKeepsVerifiedSessionPath(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/srv/project"
	sessionID := "codex-cache"
	ownPath := filepath.Join(layout.codex, "2026", "08", "09", "rollout-own.jsonl")
	writeTestFile(t, ownPath, `{"type":"session_meta","payload":{"id":"codex-cache","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"own"}}
`)
	reader := newTestReader(t, layout, nil)
	request := Request{Backend: BackendCodex, ProviderSessionID: sessionID, Workdir: workdir}
	if _, err := reader.Read(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	foreignPath := filepath.Join(layout.codex, "2026", "08", "10", "rollout-foreign.jsonl")
	writeTestFile(t, foreignPath, `{"type":"session_meta","payload":{"id":"other-session","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"foreign"}}
`)

	events, err := reader.Read(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Text != "own" {
		t.Fatalf("cache selected the wrong transcript: %#v", events)
	}
	key := resolveCacheKey{backend: BackendCodex, sessionID: sessionID, workdir: workdir}
	reader.resolveMu.Lock()
	cached := reader.resolveCache[key].path
	reader.resolveMu.Unlock()
	resolvedOwn, err := filepath.EvalSymlinks(ownPath)
	if err != nil {
		t.Fatal(err)
	}
	if cached != resolvedOwn {
		t.Fatalf("cached path = %q, want %q", cached, resolvedOwn)
	}
}

func TestCodexResolveCacheInvalidatesDeletedPathAndFindsReplacement(t *testing.T) {
	layout := newTestLayout(t)
	request := Request{Backend: BackendCodex, ProviderSessionID: "codex-moved", Workdir: "/srv/project"}
	oldPath := filepath.Join(layout.codex, "2026", "08", "09", "rollout-old.jsonl")
	writeTestFile(t, oldPath, `{"type":"session_meta","payload":{"id":"codex-moved","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"old"}}
`)
	reader := newTestReader(t, layout, nil)
	if _, err := reader.Read(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(layout.codex, "2026", "08", "10", "rollout-new.jsonl")
	writeTestFile(t, newPath, `{"type":"session_meta","payload":{"id":"codex-moved","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"new"}}
`)
	events, err := reader.Read(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Text != "new" {
		t.Fatalf("deleted cache entry was not replaced: %#v", events)
	}
}

func TestCodexResolveCacheRejectsSymlinkReplacement(t *testing.T) {
	layout := newTestLayout(t)
	request := Request{Backend: BackendCodex, ProviderSessionID: "codex-link", Workdir: "/srv/project"}
	cachedPath := filepath.Join(layout.codex, "2026", "08", "10", "rollout-link.jsonl")
	contents := `{"type":"session_meta","payload":{"id":"codex-link","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"content"}}
`
	writeTestFile(t, cachedPath, contents)
	reader := newTestReader(t, layout, nil)
	if _, err := reader.Read(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	writeTestFile(t, outside, contents)
	if err := os.Remove(cachedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, cachedPath); err != nil {
		t.Fatal(err)
	}
	_, err := reader.Read(context.Background(), request)
	if !errors.Is(err, ErrTranscriptNotFound) {
		t.Fatalf("got %v, want transcript not found", err)
	}
}

func TestCodexResolveCacheSupportsConcurrentReaders(t *testing.T) {
	layout := newTestLayout(t)
	request := Request{Backend: BackendCodex, ProviderSessionID: "codex-concurrent", Workdir: "/srv/project"}
	path := filepath.Join(layout.codex, "2026", "08", "10", "rollout-concurrent.jsonl")
	writeTestFile(t, path, `{"type":"session_meta","payload":{"id":"codex-concurrent","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"ok"}}
`)
	reader := newTestReader(t, layout, nil)
	var wait sync.WaitGroup
	errorsFound := make(chan error, 24)
	for range 24 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			events, err := reader.Read(context.Background(), request)
			if err == nil && (len(events) != 1 || events[0].Text != "ok") {
				err = errors.New("unexpected concurrent read result")
			}
			if err != nil {
				errorsFound <- err
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func TestCodexNegativeCacheStillHonorsCancellation(t *testing.T) {
	layout := newTestLayout(t)
	reader := newTestReader(t, layout, nil)
	request := Request{Backend: BackendCodex, ProviderSessionID: "missing", Workdir: "/srv/project"}
	if _, err := reader.Read(context.Background(), request); !errors.Is(err, ErrTranscriptNotFound) {
		t.Fatalf("prime negative cache: got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.Read(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context canceled", err)
	}
}
