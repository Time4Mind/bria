package transcript

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	content := `{"type":"session_meta","payload":{"id":"codex-id","cwd":` + quoteJSON(workdir) + `,"timestamp":"2026-01-02T03:04:05.600Z"}}` + "\n" +
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
	if discovery.Total != 1 || len(items) != 1 || items[0].ProviderSessionID != "codex-id" ||
		items[0].Summary != "Build the parser" ||
		!items[0].CreatedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 600_000_000, time.UTC)) {
		t.Fatalf("discovery=%#v", discovery)
	}
}

func TestDiscoverCodexScansOnceAndReusesResolvedPath(t *testing.T) {
	claudeRoot := t.TempDir()
	codexRoot := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "project")
	directory := filepath.Join(codexRoot, "2026", "01", "02")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"session_meta","payload":{"id":"codex-id","cwd":` + quoteJSON(workdir) + `}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"Build the parser"}}` + "\n"
	if err := os.WriteFile(filepath.Join(directory, "rollout-test.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(Config{ClaudeProjectsRoot: claudeRoot, CodexSessionsRoot: codexRoot})
	if err != nil {
		t.Fatal(err)
	}
	originalScan := reader.scanCodex
	var scans atomic.Int32
	reader.scanCodex = func(ctx context.Context) (*codexIndexSnapshot, error) {
		scans.Add(1)
		return originalScan(ctx)
	}

	first, err := reader.Discover(context.Background(), BackendCodex, workdir, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.Discover(context.Background(), BackendCodex, workdir, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	events, err := reader.Read(context.Background(), Request{
		Backend: BackendCodex, ProviderSessionID: "codex-id", Workdir: workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if scans.Load() != 1 {
		t.Fatalf("Codex inventory scans = %d, want 1", scans.Load())
	}
	if first.Total != 1 || second.Total != 1 || first.Candidates[0].Summary != "Build the parser" || len(events) != 1 {
		t.Fatalf("first=%#v second=%#v events=%#v", first, second, events)
	}
}

func TestDiscoverFreshRefreshesCodexIndex(t *testing.T) {
	claudeRoot := t.TempDir()
	codexRoot := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "project")
	directory := filepath.Join(codexRoot, "2026", "01", "02")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexDiscoveryFile(t, directory, "one", workdir, "First")
	reader, err := NewReader(Config{ClaudeProjectsRoot: claudeRoot, CodexSessionsRoot: codexRoot})
	if err != nil {
		t.Fatal(err)
	}
	first, err := reader.Discover(context.Background(), BackendCodex, workdir, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	writeCodexDiscoveryFile(t, directory, "two", workdir, "Second")
	cached, err := reader.Discover(context.Background(), BackendCodex, workdir, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := reader.DiscoverFresh(context.Background(), BackendCodex, workdir, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 1 || cached.Total != 1 || fresh.Total != 2 {
		t.Fatalf("first=%#v cached=%#v fresh=%#v", first, cached, fresh)
	}
}

func TestDiscoverCodexReadsPromptBeyondIndexPreview(t *testing.T) {
	claudeRoot := t.TempDir()
	codexRoot := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "project")
	directory := filepath.Join(codexRoot, "2026", "01", "02")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	content.WriteString(`{"type":"session_meta","payload":{"id":"late","cwd":` + quoteJSON(workdir) + `}}` + "\n")
	for range 40 {
		content.WriteString(`{"type":"event_msg","payload":{"type":"token_count","info":{}}}` + "\n")
	}
	content.WriteString(`{"type":"event_msg","payload":{"type":"user_message","message":"Late prompt"}}` + "\n")
	if err := os.WriteFile(filepath.Join(directory, "rollout-late.jsonl"), []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(Config{ClaudeProjectsRoot: claudeRoot, CodexSessionsRoot: codexRoot})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := reader.Discover(context.Background(), BackendCodex, workdir, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Total != 1 || len(discovery.Candidates) != 1 || discovery.Candidates[0].Summary != "Late prompt" {
		t.Fatalf("discovery=%#v", discovery)
	}
}

func TestDiscoverCodexCoalescesConcurrentIndexScans(t *testing.T) {
	claudeRoot := t.TempDir()
	codexRoot := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "project")
	directory := filepath.Join(codexRoot, "2026", "01", "02")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexDiscoveryFile(t, directory, "one", workdir, "First")
	reader, err := NewReader(Config{ClaudeProjectsRoot: claudeRoot, CodexSessionsRoot: codexRoot})
	if err != nil {
		t.Fatal(err)
	}
	originalScan := reader.scanCodex
	started := make(chan struct{})
	release := make(chan struct{})
	var scans atomic.Int32
	reader.scanCodex = func(ctx context.Context) (*codexIndexSnapshot, error) {
		if scans.Add(1) == 1 {
			close(started)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return originalScan(ctx)
		}
	}

	const callers = 8
	errorsByCaller := make(chan error, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for range callers {
		go func() {
			defer wait.Done()
			discovery, discoverErr := reader.Discover(context.Background(), BackendCodex, workdir, 0, 8)
			if discoverErr == nil && discovery.Total != 1 {
				discoverErr = errors.New("unexpected discovery total")
			}
			errorsByCaller <- discoverErr
		}()
	}
	<-started
	close(release)
	wait.Wait()
	close(errorsByCaller)
	for discoverErr := range errorsByCaller {
		if discoverErr != nil {
			t.Fatal(discoverErr)
		}
	}
	if scans.Load() != 1 {
		t.Fatalf("concurrent Codex inventory scans = %d, want 1", scans.Load())
	}
}

func writeCodexDiscoveryFile(t *testing.T, directory, id, workdir, prompt string) {
	t.Helper()
	content := `{"type":"session_meta","payload":{"id":` + quoteJSON(id) + `,"cwd":` + quoteJSON(workdir) + `}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"user_message","message":` + quoteJSON(prompt) + `}}` + "\n"
	if err := os.WriteFile(filepath.Join(directory, "rollout-"+id+".jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
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
