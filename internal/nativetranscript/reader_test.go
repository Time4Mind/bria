package nativetranscript

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/tooltext"
)

func toolTextEquals(raw, want string) bool {
	text, truncated := tooltext.Read(raw)
	return text == want && !truncated
}

const testID = "01900000-0000-7000-8000-000000000001"

func fixture(t *testing.T, provider, body string) (Options, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "project", testID+".jsonl")
	if provider == "codex" {
		path = filepath.Join(root, "2026", "09", "05", "rollout-2026-09-05T00-00-00-"+testID+".jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return Options{Provider: provider, Root: root, SessionID: testID, Workdir: "/work"}, path
}

func TestDrainDoesNotConfuseIgnoredBatchWithEOF(t *testing.T) {
	opts, path := fixture(t, "codex", codexMeta()+strings.Repeat(`{"type":"ignored"}`+"\n", 100)+`{"type":"event_msg","payload":{"type":"user_message","message":"old"}}`+"\n")
	opts.MaxLineBytes = 128
	opts.MaxPollBytes = 256
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if events, err := r.Poll(context.Background()); err != nil || len(events) != 0 {
		t.Fatalf("old events=%#v %v", events, err)
	}
	appendFile(t, path, `{"type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t3"}}`+"\n")
	events, err := r.Poll(context.Background())
	if err != nil || len(events) != 1 || events[0].Kind != KindInterrupted || events[0].TurnID != "t3" {
		t.Fatalf("interrupt=%#v %v", events, err)
	}
}

func TestDrainBoundsAndPartialBaseline(t *testing.T) {
	opts, path := fixture(t, "codex", codexMeta()+`{"type":"ignored"}`)
	opts.MaxLineBytes = 128
	opts.MaxPollBytes = 256
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.Drain(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial=%v", err)
	}
	appendFile(t, path, "\n")
	if err := r.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, strings.Repeat(`{"type":"ignored"}`+"\n", 3000))
	if err := r.Drain(context.Background()); !errors.Is(err, ErrLimit) {
		t.Fatalf("unbounded drain=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Drain(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func codexMeta() string {
	return `{"type":"session_meta","payload":{"id":"` + testID + `","cwd":"/work"}}` + "\n"
}

func TestExactIncrementalCodexAndExplicitCompletion(t *testing.T) {
	opts, path := fixture(t, "codex", codexMeta()+`{"type":"turn_context","payload":{"turn_id":"t1","model":"gpt-test"}}`+"\n"+`{"type":"event_msg","payload":{"type":"user_message","message":"hello"}}`+"\n"+`{"type":"event_msg","payload":{"type":"agent_message","message":"done","phase":"final"}}`+"\n")
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	events, err := r.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Kind != KindModel || events[1].Kind != KindUser || events[2].Kind != KindFinal || events[2].TurnID != "t1" {
		t.Fatalf("events=%#v", events)
	}
	if next, err := r.Poll(context.Background()); err != nil || len(next) != 0 {
		t.Fatalf("replay=%#v %v", next, err)
	}
	appendFile(t, path, `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`)
	if next, err := r.Poll(context.Background()); err != nil || len(next) != 0 {
		t.Fatalf("partial=%#v %v", next, err)
	}
	appendFile(t, path, "\n")
	next, err := r.Poll(context.Background())
	if err != nil || len(next) != 1 || next[0].Kind != KindComplete {
		t.Fatalf("complete=%#v %v", next, err)
	}
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsWrongBindingDuplicateAndReplacement(t *testing.T) {
	opts, path := fixture(t, "codex", codexMeta())
	wrong := opts
	wrong.Workdir = "/other"
	if _, err := Open(context.Background(), wrong); !errors.Is(err, ErrBinding) {
		t.Fatalf("wrong cwd %v", err)
	}
	duplicate := filepath.Join(opts.Root, "other-"+testID+".jsonl")
	if err := os.WriteFile(duplicate, []byte(codexMeta()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), opts); !errors.Is(err, ErrBinding) {
		t.Fatalf("duplicate %v", err)
	}
	if err := os.Remove(duplicate); err != nil {
		t.Fatal(err)
	}
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(codexMeta()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Poll(context.Background()); !errors.Is(err, ErrBinding) {
		t.Fatalf("replacement %v", err)
	}
}

func TestClaudePreservesStructuredToolLifecycleWithoutTreatingResultAsPrompt(t *testing.T) {
	prefix := `"sessionId":"` + testID + `","cwd":"/work",`
	body := `{"type":"user",` + prefix + `"uuid":"u1","message":{"content":"question"}}` + "\n" +
		`{"type":"assistant",` + prefix + `"message":{"model":"claude-test","stop_reason":"tool_use","content":[{"type":"text","text":"reading"},{"type":"tool_use","id":"tool-1","name":"Read","input":{"path":"README.md"}}]}}` + "\n" +
		`{"type":"user",` + prefix + `"message":{"content":[{"type":"tool_result","tool_use_id":"tool-1","content":"ok"}]}}` + "\n" +
		`{"type":"assistant",` + prefix + `"message":{"stop_reason":"end_turn","content":[{"type":"text","text":"answer"}]}}` + "\n"
	opts, _ := fixture(t, "claude", body)
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	events, err := r.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Kind{KindUser, KindModel, KindCommentary, KindTool, KindTool, KindFinal, KindComplete}
	if len(events) != len(want) {
		t.Fatalf("events=%#v", events)
	}
	for i, k := range want {
		if events[i].Kind != k {
			t.Fatalf("event %d = %#v", i, events[i])
		}
	}
	if events[3].Text != "Read" || events[3].Metadata == nil || events[3].Metadata.ItemID != "tool-1" ||
		!toolTextEquals(events[3].Metadata.Arguments, `{"path":"README.md"}`) || events[3].Metadata.Status != "in_progress" ||
		events[4].Metadata == nil || events[4].Metadata.ItemID != "tool-1" || !toolTextEquals(events[4].Metadata.Result, "ok") ||
		events[4].Metadata.Status != "completed" || events[6].TurnID != "u1" {
		t.Fatalf("tools/turn=%#v", events)
	}
}

func TestCancellationAndNoFilenameGuessing(t *testing.T) {
	opts, path := fixture(t, "codex", codexMeta())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(filepath.Dir(path), "newest.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), opts); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestPaginatedCodexAcceptsOnlyUserTextAndPhasedResponses(t *testing.T) {
	opts, _ := fixture(t, "codex", codexMeta()+`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"private instructions"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["agents_md.instructions"]}}}`+"\n"+
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}],"internal_chat_message_metadata_passthrough":{"turn_id":"t2","content_item_kinds":["user.text"]}}}`+"\n"+
		`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"working"}]}}`+"\n"+
		`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"done"}]}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"agent_message","phase":"final_answer","message":"done"}}`+"\n")
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	events, err := r.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Kind != KindUser || events[0].Text != "hi" || events[0].TurnID != "t2" || events[1].Kind != KindCommentary || events[2].Kind != KindFinal {
		t.Fatalf("events=%#v", events)
	}
}

func TestCodexPreservesThinkingAndStructuredToolLifecycle(t *testing.T) {
	body := codexMeta() +
		`{"type":"turn_context","payload":{"turn_id":"t1","model":"gpt-test"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"user_message","turn_id":"t1","message":"inspect"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"agent_reasoning","turn_id":"t1","text":"checking files"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"function_call","call_id":"call-1","name":"read_file","arguments":"{\"path\":\"README.md\"}","status":"in_progress"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"ok","status":"completed"}}` + "\n"
	opts, _ := fixture(t, "codex", body)
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	events, err := r.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 || events[2].Kind != KindThinking || events[2].Text != "checking files" ||
		events[3].Kind != KindTool || events[3].Metadata == nil || events[3].Metadata.ItemID != "call-1" ||
		events[3].Metadata.Name != "read_file" || !toolTextEquals(events[3].Metadata.Arguments, `{"path":"README.md"}`) || events[3].Metadata.Status != "in_progress" ||
		events[4].Metadata == nil || events[4].Metadata.ItemID != "call-1" || !toolTextEquals(events[4].Metadata.Result, "ok") || events[4].Metadata.Status != "completed" {
		t.Fatalf("events=%#v", events)
	}
}

func TestBoundsMalformedTruncationAndSymlink(t *testing.T) {
	t.Run("duplicate identity", func(t *testing.T) {
		opts, _ := fixture(t, "codex", `{"type":"session_meta","payload":{"id":"foreign","id":"`+testID+`","cwd":"/work"}}`+"\n")
		if _, err := Open(context.Background(), opts); !errors.Is(err, ErrMalformed) {
			t.Fatal(err)
		}
	})
	t.Run("line", func(t *testing.T) {
		opts, _ := fixture(t, "codex", codexMeta()+`{"type":"ignored","padding":"`+string(make([]byte, 200))+`"}`+"\n")
		opts.MaxLineBytes = 128
		opts.MaxPollBytes = 512
		r, err := Open(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if _, err := r.Poll(context.Background()); !errors.Is(err, ErrLimit) {
			t.Fatal(err)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		opts, path := fixture(t, "codex", codexMeta()+"not JSON\n")
		r, err := Open(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if _, err := r.Poll(context.Background()); !errors.Is(err, ErrMalformed) {
			t.Fatal(err)
		}
		// Failed batches must not advance past earlier valid records.
		if r.offset != 0 {
			t.Fatal("committed failed batch")
		}
		if err := os.WriteFile(path, []byte(codexMeta()), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Poll(context.Background()); !errors.Is(err, ErrBinding) {
			t.Fatal(err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		opts, path := fixture(t, "codex", codexMeta())
		if err := os.Rename(path, path+".actual"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path+".actual", path); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), opts); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	})
	t.Run("entries", func(t *testing.T) {
		opts, _ := fixture(t, "codex", codexMeta())
		opts.MaxEntries = 1
		if _, err := Open(context.Background(), opts); !errors.Is(err, ErrLimit) {
			t.Fatal(err)
		}
	})
}
