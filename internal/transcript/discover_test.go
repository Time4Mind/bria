package transcript

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverClaudeSessionsForExactWorkdir(t *testing.T) {
	root := t.TempDir()
	codexRoot := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, encodeClaudeWorkdir(workdir))
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	row := `{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"content":[{"type":"text","text":"Investigate the queue"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(project, "claude-id.jsonl"), []byte(row), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(Config{ClaudeProjectsRoot: root, CodexSessionsRoot: codexRoot})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := reader.Discover(context.Background(), BackendClaude, workdir, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	items := discovery.Candidates
	if discovery.Total != 1 || len(items) != 1 || items[0].ProviderSessionID != "claude-id" || items[0].Summary != "Investigate the queue" {
		t.Fatalf("discovery=%#v", discovery)
	}
}

func TestDiscoverPaginatesCandidatesAndReportsTotal(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, encodeClaudeWorkdir(workdir))
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 10; index++ {
		name := fmt.Sprintf("session-%02d.jsonl", index)
		if err := os.WriteFile(filepath.Join(project, name), []byte(`{"type":"user","message":{"content":"hello"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := NewReader(Config{ClaudeProjectsRoot: root, CodexSessionsRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := reader.Discover(context.Background(), BackendClaude, workdir, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Total != 10 || len(discovery.Candidates) != 2 {
		t.Fatalf("second page=%#v", discovery)
	}
}

func TestDiscoverCodexUsesSessionMetadataWorkdir(t *testing.T) {
	claudeRoot := t.TempDir()
	codexRoot := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "project")
	directory := filepath.Join(codexRoot, "2026", "01", "02")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"session_meta","payload":{"id":"codex-id","cwd":` + quoteJSON(workdir) + `}}` + "\n" +
		`{"type":"event_msg","timestamp":"2026-01-02T00:00:00Z","payload":{"type":"user_message","message":"Build the parser"}}` + "\n"
	if err := os.WriteFile(filepath.Join(directory, "rollout-test.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(Config{ClaudeProjectsRoot: claudeRoot, CodexSessionsRoot: codexRoot})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := reader.Discover(context.Background(), BackendCodex, workdir, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	items := discovery.Candidates
	if discovery.Total != 1 || len(items) != 1 || items[0].ProviderSessionID != "codex-id" || items[0].Summary != "Build the parser" {
		t.Fatalf("discovery=%#v", discovery)
	}
}

func quoteJSON(value string) string {
	encoded := make([]byte, 0, len(value)+2)
	encoded = append(encoded, '"')
	for _, char := range []byte(value) {
		if char == '\\' || char == '"' {
			encoded = append(encoded, '\\')
		}
		encoded = append(encoded, char)
	}
	encoded = append(encoded, '"')
	return string(encoded)
}
