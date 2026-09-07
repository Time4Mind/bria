package nativetranscript

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeNativeTitleExactIdentityAndCustomPriority(t *testing.T) {
	header := `{"type":"user","sessionId":"` + testID + `","cwd":"/work","uuid":"u1","message":{"content":"first prompt must not be title"}}` + "\n"
	opts, path := fixture(t, "claude", header+`{"type":"custom-title","sessionId":"`+testID+`","customTitle":"CLI title"}`+"\n"+`{"type":"ai-title","sessionId":"`+testID+`","aiTitle":"Later AI"}`+"\n")
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if title, err := r.Title(context.Background()); err != nil || title != "CLI title" {
		t.Fatal("baseline custom title missing or overwritten")
	}
	appendFile(t, path, `{"type":"custom-title","sessionId":"foreign","customTitle":"Foreign"}`+"\n")
	if _, err := r.Poll(context.Background()); err == nil {
		t.Fatal("foreign title identity accepted")
	}
}

func TestCodexTitleBoundedIndexExactLatestAndNoPromptFallback(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "sessions")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rollout-"+testID+".jsonl"), []byte(codexMeta()), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(context.Background(), Options{Provider: "codex", Root: root, SessionID: testID, Workdir: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if title, err := r.Title(context.Background()); err != nil || title != "" {
		t.Fatal("missing name index fabricated title")
	}
	index := filepath.Join(home, "session_index.jsonl")
	body := strings.Repeat(`{"id":"foreign","thread_name":"Other"}`+"\n", 20000) + `{"id":"` + testID + `","thread_name":"Own title"}` + "\n"
	if err := os.WriteFile(index, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if title, err := r.Title(context.Background()); err != nil || title != "Own title" {
			t.Fatal("bounded exact index title missing")
		}
	}
	appendFile(t, index, `{"id":"`+testID+`","thread_name":"Renamed"}`+"\n")
	if title, err := r.Title(context.Background()); err != nil || title != "Renamed" {
		t.Fatal("appended native rename missed")
	}
}
