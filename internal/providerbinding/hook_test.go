package providerbinding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestCaptureBindsOnlyExactBriaTmuxWindow(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(filepath.Join(directory, "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(directory, "workspace")
	if err := os.Mkdir(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005482"
	transcriptPath := filepath.Join(directory, "rollout.jsonl")
	meta := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}`+"\n", providerID, workdir)
	if err := os.WriteFile(transcriptPath, []byte(meta), 0600); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{
		"session_id":%q,"cwd":%q,"hook_event_name":"SessionStart",
		"transcript_path":%q,"future_field":true
	}`, providerID, workdir, transcriptPath)
	environment := map[string]string{
		EnvNodeID: "mac", EnvSessionID: "bria-session",
		EnvTmuxSession: "bria-standalone", EnvTmuxWindow: "bria-window", "TMUX_PANE": "%9",
	}
	getenv := func(key string) string { return environment[key] }
	now := time.Unix(100, 0).UTC()
	if err := Capture(context.Background(), store, strings.NewReader(payload), getenv,
		func(context.Context, string) (string, error) {
			return "ccbot\t\tforeign-window", nil
		}, func() time.Time { return now }); err == nil {
		t.Fatal("foreign tmux window was accepted")
	}
	if _, found, err := store.Lookup(domain.SessionRef{NodeID: "mac", SessionID: "bria-session"}, workdir); err != nil || found {
		t.Fatalf("foreign binding persisted: found=%v err=%v", found, err)
	}
	if err := Capture(context.Background(), store, strings.NewReader(payload), getenv,
		func(context.Context, string) (string, error) {
			return "bria-standalone\t\tbria-window", nil
		}, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.Lookup(domain.SessionRef{NodeID: "mac", SessionID: "bria-session"}, workdir)
	if err != nil || !found || record.ProviderSessionID != providerID || record.UpdatedAt != now {
		t.Fatalf("record=%#v found=%v err=%v", record, found, err)
	}
}

func TestCaptureIgnoresProviderOutsideBria(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"session_id":"019fffe8-02ee-7aa1-b6cf-eed13a005482","cwd":"/tmp","hook_event_name":"SessionStart","transcript_path":"/tmp/rollout.jsonl"}`
	if err := Capture(context.Background(), store, strings.NewReader(payload), func(string) string { return "" },
		func(context.Context, string) (string, error) { t.Fatal("tmux lookup called"); return "", nil }, time.Now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.path); !os.IsNotExist(err) {
		t.Fatalf("foreign hook created a binding store: %v", err)
	}
}

func TestInstallHookPreservesExistingHooksAndIsIdempotent(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "hooks.json")
	existing := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"ccbot hook","timeout":5}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := InstallHook("/opt/bria", "/var/bria config.json", path); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	if strings.Count(encoded, "provider-hook") != 3 || strings.Count(encoded, "ccbot hook") != 1 {
		t.Fatalf("installed hooks are not preserved/idempotent: %s", encoded)
	}
}

func TestInstallHookReplacesStaleBriaAcrossEnvironments(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "hooks.json")
	current := "/opt/bria/current/bria"
	config := "/var/bria/config.json"
	existing := fmt.Sprintf(`{"hooks":{"SessionStart":[{"hooks":[
		{"type":"command","command":"ccbot hook","timeout":5},
		{"type":"command","command":%q,"timeout":5},
		{"type":"command","command":%q,"timeout":5}
	]}]}}`,
		shellQuote("/opt/bria/dev/bria")+" provider-hook --config "+shellQuote(config),
		shellQuote("/opt/bria/other/bria")+" provider-hook --config "+shellQuote("/var/bria/other.json"),
	)
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InstallHook(current, config, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	if strings.Contains(encoded, "/opt/bria/dev/bria") {
		t.Fatalf("stale Bria hook survived: %s", encoded)
	}
	if strings.Count(encoded, current) != len(codexHookEvents) {
		t.Fatalf("current Bria hook count is not canonical: %s", encoded)
	}
	if !strings.Contains(encoded, "ccbot hook") {
		t.Fatalf("unrelated tool hook was removed: %s", encoded)
	}
	if strings.Contains(encoded, "/var/bria/other.json") {
		t.Fatalf("stale Bria environment survived: %s", encoded)
	}
}

func TestCaptureStopReturnsOneFinalWakeSignal(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(filepath.Join(directory, "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(directory, "workspace")
	if err := os.Mkdir(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005482"
	transcriptPath := filepath.Join(directory, "rollout.jsonl")
	meta := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}`+"\n", providerID, workdir)
	if err := os.WriteFile(transcriptPath, []byte(meta), 0600); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{
		"session_id":%q,"cwd":%q,"hook_event_name":"Stop",
		"transcript_path":%q
	}`, providerID, workdir, transcriptPath)
	environment := map[string]string{
		EnvNodeID: "mac", EnvSessionID: "bria-session",
		EnvTmuxSession: "bria", EnvTmuxWindow: "window", "TMUX_PANE": "%1",
	}
	result, err := CaptureEvent(
		context.Background(), store, strings.NewReader(payload),
		func(key string) string { return environment[key] },
		func(context.Context, string) (string, error) { return "bria\t\twindow", nil },
		time.Now, "codex",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.WakeFinal || result.NodeID != "mac" || result.SessionID != "bria-session" ||
		result.ProviderSessionID != providerID {
		t.Fatalf("hook result=%#v", result)
	}
}

func TestInstallHooksAddsProviderSpecificCompletionEvents(t *testing.T) {
	directory := t.TempDir()
	codexPath := filepath.Join(directory, ".codex", "hooks.json")
	claudePath := filepath.Join(directory, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudePath, []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks("/opt/bria", "/var/bria.json", codexPath, claudePath); err != nil {
		t.Fatal(err)
	}
	for path, events := range map[string][]string{
		codexPath:  {"SessionStart", "UserPromptSubmit", "Stop"},
		claudePath: {"SessionStart", "UserPromptSubmit", "Stop", "StopFailure"},
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Hooks map[string][]any `json:"hooks"`
		}
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if len(document.Hooks[event]) != 1 {
				t.Fatalf("%s event %s=%#v", path, event, document.Hooks[event])
			}
		}
		if path == claudePath && (!strings.Contains(string(data), "--backend") ||
			!strings.Contains(string(data), "claude")) {
			t.Fatalf("Claude backend missing from hook command: %s", data)
		}
	}
}
