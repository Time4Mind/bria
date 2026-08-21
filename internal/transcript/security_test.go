package transcript

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReaderRejectsTraversalAndRelativeWorkdir(t *testing.T) {
	layout := newTestLayout(t)
	reader := newTestReader(t, layout, nil)
	requests := []Request{
		{Backend: BackendClaude, ProviderSessionID: "../secret", Workdir: "/safe"},
		{Backend: BackendClaude, ProviderSessionID: "session", Workdir: "relative"},
		{Backend: Backend("other"), ProviderSessionID: "session", Workdir: "/safe"},
	}
	for _, request := range requests {
		_, err := reader.Read(context.Background(), request)
		if request.Backend == Backend("other") {
			if !errors.Is(err, ErrUnsupportedBackend) {
				t.Errorf("got %v, want unsupported backend", err)
			}
			continue
		}
		if !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("got %v, want invalid request", err)
		}
	}
}

func TestClaudeResolverRejectsTranscriptSymlink(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/safe"
	sessionID := "session"
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	writeTestFile(t, outside, `{"type":"user","message":{"content":"secret"}}`)
	candidate := filepath.Join(layout.claude, encodeClaudeWorkdir(workdir), sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, candidate); err != nil {
		t.Fatal(err)
	}
	_, err := newTestReader(t, layout, nil).Read(context.Background(), Request{
		Backend: BackendClaude, ProviderSessionID: sessionID, Workdir: workdir,
	})
	if !errors.Is(err, ErrUnsafeTranscript) {
		t.Fatalf("got %v, want unsafe transcript", err)
	}
}

func TestMalformedPartialAndOversizedJSONLLinesAreSkipped(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/safe"
	sessionID := "session"
	path := filepath.Join(layout.claude, encodeClaudeWorkdir(workdir), sessionID+".jsonl")
	oversized := strings.Repeat("x", 300)
	writeTestFile(t, path, `{"type":"user","message":{"content":"before"}}
`+oversized+`
not-json
{"type":"assistant","message":{"stop_reason":"end_turn","content":"after"}}
{"type":"assistant","message":{"content":"partial"}`)
	reader := newTestReader(t, layout, func(config *Config) {
		config.MaxLineBytes = 256
		config.MaxReadBytes = 4096
	})
	events, err := reader.Read(context.Background(), Request{
		Backend: BackendClaude, ProviderSessionID: sessionID, Workdir: workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Text != "before" || events[1].Text != "after" {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestRecentReadWindowDropsPartialLeadingLine(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/safe"
	sessionID := "session"
	path := filepath.Join(layout.claude, encodeClaudeWorkdir(workdir), sessionID+".jsonl")
	writeTestFile(t, path, `{"type":"user","message":{"content":"`+strings.Repeat("old", 100)+`"}}
{"type":"assistant","message":{"stop_reason":"end_turn","content":"recent"}}
`)
	reader := newTestReader(t, layout, func(config *Config) { config.MaxReadBytes = 100 })
	events, err := reader.Read(context.Background(), Request{
		Backend: BackendClaude, ProviderSessionID: sessionID, Workdir: workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Text != "recent" {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestFirstUserTextReadsBeforeRecentWindow(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/safe"
	sessionID := "session"
	path := filepath.Join(layout.claude, encodeClaudeWorkdir(workdir), sessionID+".jsonl")
	writeTestFile(t, path, `{"type":"user","message":{"content":"actual first prompt"}}
{"type":"assistant","message":{"stop_reason":"end_turn","content":"`+strings.Repeat("recent", 100)+`"}}
`)
	reader := newTestReader(t, layout, func(config *Config) { config.MaxReadBytes = 100 })
	request := Request{
		Backend: BackendClaude, ProviderSessionID: sessionID, Workdir: workdir,
	}
	events, err := reader.Read(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Text == "actual first prompt" {
			t.Fatal("recent window unexpectedly retained the first prompt")
		}
	}
	text, err := reader.ReadFirstUserText(context.Background(), request)
	if err != nil || text != "actual first prompt" {
		t.Fatalf("first user text=%q err=%v", text, err)
	}
}

func TestFirstUserTextsReturnFirstThreeFromTranscriptStart(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/safe"
	sessionID := "session-three"
	path := filepath.Join(layout.claude, encodeClaudeWorkdir(workdir), sessionID+".jsonl")
	writeTestFile(t, path, `{"type":"user","message":{"content":"first"}}
{"type":"assistant","message":{"content":"skip"}}
{"type":"user","message":{"content":"second"}}
{"type":"user","message":{"content":"third"}}
{"type":"user","message":{"content":"fourth"}}
`)
	reader := newTestReader(t, layout, nil)
	texts, err := reader.ReadFirstUserTexts(context.Background(), Request{
		Backend: BackendClaude, ProviderSessionID: sessionID, Workdir: workdir,
	}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(texts, ","); got != "first,second,third" {
		t.Fatalf("first user texts=%q", texts)
	}
	if _, err := reader.ReadFirstUserTexts(context.Background(), Request{
		Backend: BackendClaude, ProviderSessionID: sessionID, Workdir: workdir,
	}, 4); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("limit error=%v", err)
	}
}

func TestBodiesAreBoundedWithoutBrokenUTF8(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/safe"
	sessionID := "session"
	path := filepath.Join(layout.claude, encodeClaudeWorkdir(workdir), sessionID+".jsonl")
	writeTestFile(t, path, `{"type":"user","message":{"content":"абвг"}}
`)
	reader := newTestReader(t, layout, func(config *Config) { config.MaxBodyBytes = 5 })
	events, err := reader.Read(context.Background(), Request{
		Backend: BackendClaude, ProviderSessionID: sessionID, Workdir: workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Text != "аб" {
		t.Fatalf("unexpected bounded UTF-8: %#v", events)
	}
}

func TestConfigRejectsNonPositiveLimits(t *testing.T) {
	layout := newTestLayout(t)
	_, err := NewReader(Config{
		ClaudeProjectsRoot: layout.claude,
		CodexSessionsRoot:  layout.codex,
		MaxEvents:          -1,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("got %v, want invalid request", err)
	}
}
