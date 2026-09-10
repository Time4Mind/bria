package promptpreprocesssession

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/promptpreprocess"
	"bria/internal/promptpreprocesscommand"
	providercodex "bria/internal/provider/codex"
)

func TestMain(m *testing.M) {
	if os.Getenv("PREPROCESS_RESUME_ADAPTER_FIXTURE") == "1" {
		runResumeAdapterFixture()
		os.Exit(0)
	}
	if os.Getenv("PREPROCESS_ADAPTER_HANG_CLOSE") == "1" {
		runHangingCloseAdapterFixture()
		os.Exit(0)
	}
	if os.Getenv("PREPROCESS_ADAPTER_FIXTURE") == "1" {
		rawEnvironment := make([]string, 0, len(os.Environ()))
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "PREPROCESS_ADAPTER_FIXTURE=") {
				rawEnvironment = append(rawEnvironment, entry)
			}
		}
		err := providercodex.RunAdapter(context.Background(), os.Stdin, os.Stdout, providercodex.AdapterConfig{
			RawCommand: []string{mustAbs(os.Args[0]), "app-server"}, RawEnv: rawEnvironment, Workdir: mustGetwd(),
			ThreadApprovalPolicy: "never", ThreadSandbox: "read-only", ThreadEphemeral: true,
			RequireReadOnly: true, RejectInteractions: true,
			ClientInfo: providercodex.ClientInfo{Name: "bria-preprocess-test", Version: "1"},
		})
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(41)
		}
		os.Exit(0)
	}
	if os.Getenv("PREPROCESS_APP_SERVER_FIXTURE") == "1" {
		runAppServerFixture()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestCodexSessionIsReadyProcessesMultipleTurnsAndCloses(t *testing.T) {
	store := testConfigStore(os.Args[0])
	commands, err := promptpreprocesscommand.New(store, append(os.Environ(), "PREPROCESS_APP_SERVER_FIXTURE=1", "PREPROCESS_ADAPTER_FIXTURE=1", "BRIA_TEST_TELEGRAM_TOKEN=redacted"), "local")
	if err != nil {
		t.Fatal(err)
	}
	selection, err := commands.Select(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	current, err := startCodexSession(ctx, selection, os.Args[0], "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := current.Process(ctx, promptpreprocess.Request{
		ComputerID: "local", SessionID: "session", MessageID: "message", Sequence: 1,
		Instruction: "clean", Text: "raw",
	})
	if err != nil || result.Text != "cleaned fixture" || result.ModelEvidence != "" {
		t.Fatalf("result = (%#v, %v)", result, err)
	}
	result, err = current.Process(ctx, promptpreprocess.Request{
		ComputerID: "local", SessionID: "session", MessageID: "message-2", Sequence: 2,
		Instruction: "clean", Text: "raw again",
	})
	if err != nil || result.Text != "cleaned fixture" {
		t.Fatalf("second result = (%#v, %v)", result, err)
	}
	if err := current.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCodexSessionPassesExactResumeBindingToAdapter(t *testing.T) {
	store := testConfigStore(os.Args[0])
	commands, err := promptpreprocesscommand.New(store, append(os.Environ(),
		"PREPROCESS_RESUME_ADAPTER_FIXTURE=1", "PREPROCESS_EXPECT_RESUME_THREAD_ID=thread-persisted",
		"BRIA_TEST_TELEGRAM_TOKEN=redacted"), "local")
	if err != nil {
		t.Fatal(err)
	}
	selection, err := commands.Select(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	current, err := startCodexSession(ctx, selection, os.Args[0], "thread-persisted")
	if err != nil {
		t.Fatal(err)
	}
	if current.Binding() != "thread-persisted" {
		t.Fatalf("binding = %q", current.Binding())
	}
	if err := current.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSatelliteWorkdirIsStablePerTopologyAndPrivate(t *testing.T) {
	shared := BindingKey{Mode: ModeShared}
	first, err := satelliteWorkdir("computer-a", shared)
	if err != nil {
		t.Fatal(err)
	}
	again, err := satelliteWorkdir("computer-a", shared)
	if err != nil || again != first {
		t.Fatalf("stable workdir = (%q, %v), want %q", again, err, first)
	}
	personal, err := satelliteWorkdir("computer-a", BindingKey{Mode: ModePerSession, SessionID: "session-a"})
	if err != nil || personal == first {
		t.Fatalf("personal workdir = (%q, %v), shared %q", personal, err, first)
	}
	private := filepath.Join(t.TempDir(), "satellite")
	if err := prepareSatelliteWorkdir(private); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(private)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("workdir info = (%#v, %v)", info, err)
	}
}

func TestCodexSessionStartupCancellationStopsSilentAdapterTree(t *testing.T) {
	store := testConfigStore(os.Args[0])
	commands, err := promptpreprocesscommand.New(store, append(os.Environ(),
		"PREPROCESS_APP_SERVER_FIXTURE=1", "PREPROCESS_APP_SERVER_HANG_START=1",
		"PREPROCESS_ADAPTER_FIXTURE=1", "BRIA_TEST_TELEGRAM_TOKEN=redacted"), "local")
	if err != nil {
		t.Fatal(err)
	}
	selection, err := commands.Select(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if current, startErr := startCodexSession(ctx, selection, os.Args[0], ""); startErr == nil || current != nil {
		t.Fatalf("silent adapter startup = (%#v, %v)", current, startErr)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("startup cancellation did not stop the adapter tree promptly")
	}
}

func TestCodexSessionCloseKillsUnresponsiveExternalAdapter(t *testing.T) {
	store := testConfigStore(os.Args[0])
	commands, err := promptpreprocesscommand.New(store, append(os.Environ(),
		"PREPROCESS_ADAPTER_HANG_CLOSE=1", "BRIA_TEST_TELEGRAM_TOKEN=redacted"), "local")
	if err != nil {
		t.Fatal(err)
	}
	selection, err := commands.Select(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current, err := startCodexSession(context.Background(), selection, os.Args[0], "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.Process(context.Background(), promptpreprocess.Request{
		ComputerID: "local", SessionID: "session", MessageID: "message", Sequence: 1,
		Instruction: "clean", Text: "raw",
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := current.Close(ctx); err == nil {
		t.Fatal("unresponsive adapter close unexpectedly succeeded")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("unresponsive adapter tree was not killed promptly")
	}
}

func TestLiveLunaPersistentSatellite(t *testing.T) {
	if os.Getenv("BRIA_LIVE_PREPROCESS_SESSION") != "1" {
		t.Skip("live provider acceptance is opt-in")
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	commands, err := promptpreprocesscommand.New(testConfigStore(path), append(os.Environ(), "BRIA_TEST_TELEGRAM_TOKEN=redacted"), "local")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := filepath.Abs(filepath.Join("..", "..", "bin", "bria-codex-adapter"))
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := OpenFileBindingStore(filepath.Join(t.TempDir(), "satellites.json"))
	if err != nil {
		t.Fatal(err)
	}
	processor, err := NewWithBindingStore(commands, adapter, bindings)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	processor.Warmup(ctx)
	first := promptpreprocess.Request{
		ComputerID: "local", SessionID: "live-test", MessageID: "live-message", Sequence: 1,
		Mode:        promptpreprocess.ModeShared,
		Instruction: "Исправь только очевидные ошибки. Верни только готовый запрос.",
		Text:        "проверь пажалуста что тесты проходят",
	}
	liveProcessAndAccept(t, ctx, processor, first)
	sharedKey := BindingKey{Mode: ModeShared}
	before, found, err := bindings.Load(ctx, sharedKey)
	if err != nil || !found || before.ProviderSessionID == "" {
		t.Fatalf("first shared binding = (%#v, %t, %v)", before, found, err)
	}

	second := first
	second.SessionID = "live-test-other-primary"
	second.MessageID = "live-message-2"
	second.Sequence = 2
	second.Text = "исправь пажалуста второй запрос"
	liveProcessAndAccept(t, ctx, processor, second)
	afterSecond, found, err := bindings.Load(ctx, sharedKey)
	if err != nil || !found || afterSecond.ProviderSessionID != before.ProviderSessionID {
		t.Fatalf("shared thread changed between primary sessions: before=%#v after=%#v found=%t err=%v", before, afterSecond, found, err)
	}
	if err := processor.Close(ctx); err != nil {
		t.Fatal(err)
	}

	resumed, err := NewWithBindingStore(commands, adapter, bindings)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(context.Background())
	third := first
	third.MessageID = "live-message-after-restart"
	third.Sequence = 3
	third.Text = "исправь пажалуста запрос после перезапуска"
	liveProcessAndAccept(t, ctx, resumed, third)
	afterRestart, found, err := bindings.Load(ctx, sharedKey)
	if err != nil || !found || afterRestart.ProviderSessionID != before.ProviderSessionID {
		t.Fatalf("shared thread was not resumed exactly: before=%#v after=%#v found=%t err=%v", before, afterRestart, found, err)
	}
}

func liveProcessAndAccept(t *testing.T, ctx context.Context, processor *Processor, request promptpreprocess.Request) {
	t.Helper()
	result, err := processor.Process(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := promptpreprocess.ValidateResult(request.Text, result.Text); err != nil {
		t.Fatal(err)
	}
	if result.Provider != domain.ProviderCodex || result.Model != "gpt-5.6-luna" || result.ModelEvidence != "" || result.Completion == nil {
		t.Fatalf("receipt = %#v", result)
	}
	if err := result.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func mustGetwd() string {
	workdir, err := os.Getwd()
	if err != nil {
		os.Exit(42)
	}
	return workdir
}

func mustAbs(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		os.Exit(43)
	}
	return absolute
}

func testConfigStore(executable string) staticConfigStore {
	return staticConfigStore{snapshot: config.Snapshot{Revision: 1, Config: config.Config{
		OwnerUserID: 1, PrivateChatID: 1, BotUsername: "test_bot",
		StatePath:     "/tmp/bria-preprocess-test-state",
		TelegramToken: config.TelegramTokenRef{EnvVar: "BRIA_TEST_TELEGRAM_TOKEN"},
		CallbackKey:   config.CallbackKeyRef{SecretFile: "/tmp/bria-preprocess-test-callback"},
		Providers: map[string]config.ProviderConfig{
			string(domain.ProviderCodex):  {Enabled: true, Command: &config.ProviderCommand{Exec: executable, Argv: []string{}}},
			string(domain.ProviderClaude): {Enabled: false},
		},
	}}}
}

type staticConfigStore struct{ snapshot config.Snapshot }

func (store staticConfigStore) Load(context.Context) (config.Config, error) {
	return store.snapshot.Config, nil
}
func (store staticConfigStore) Current(context.Context) (config.Snapshot, error) {
	return store.snapshot, nil
}
func (store staticConfigStore) Reload(context.Context) (config.Snapshot, error) {
	return store.snapshot, nil
}
func (store staticConfigStore) CompareAndSwap(context.Context, uint64, config.Config) (config.Snapshot, error) {
	return store.snapshot, nil
}
func (store staticConfigStore) Update(context.Context, uint64, func(*config.Config) error) (config.Snapshot, error) {
	return store.snapshot, nil
}
func (store staticConfigStore) SetProviderEnabled(context.Context, uint64, domain.Provider, bool) (config.Snapshot, error) {
	return store.snapshot, nil
}
func (staticConfigStore) LastReloadError() error { return nil }

func runAppServerFixture() {
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	read := func() map[string]any {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			os.Exit(31)
		}
		var message map[string]any
		if json.Unmarshal(line, &message) != nil {
			os.Exit(32)
		}
		return message
	}
	request := read()
	if request["method"] != "initialize" {
		os.Exit(33)
	}
	if os.Getenv("PREPROCESS_APP_SERVER_HANG_START") == "1" {
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	_ = encoder.Encode(map[string]any{"id": request["id"], "result": map[string]any{
		"userAgent": "codex-cli/test", "codexHome": "/redacted", "platformFamily": "unix", "platformOs": "macos",
	}})
	if read()["method"] != "initialized" {
		os.Exit(34)
	}
	request = read()
	params, _ := request["params"].(map[string]any)
	if request["method"] != "thread/start" || params["ephemeral"] != true || params["sandbox"] != "read-only" {
		os.Exit(35)
	}
	_ = encoder.Encode(map[string]any{"id": request["id"], "result": map[string]any{
		"thread":         map[string]any{"id": "preprocess-thread", "sessionId": "preprocess-session", "ephemeral": true, "cwd": params["cwd"], "status": map[string]any{"type": "idle"}},
		"approvalPolicy": "never", "sandbox": map[string]any{"type": "readOnly", "networkAccess": false},
	}})
	for turn := 1; ; turn++ {
		request = read()
		params, _ = request["params"].(map[string]any)
		if request["method"] != "turn/start" || params["threadId"] != "preprocess-thread" || params["model"] != "gpt-5.6-luna" || params["effort"] != "low" {
			os.Exit(36)
		}
		turnID := fmt.Sprintf("preprocess-turn-%d", turn)
		_ = encoder.Encode(map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": turnID, "items": []any{}, "itemsView": "notLoaded", "status": "inProgress", "error": nil}}})
		_ = encoder.Encode(map[string]any{"method": "item/completed", "params": map[string]any{
			"threadId": "preprocess-thread", "turnId": turnID,
			"item": map[string]any{"type": "agentMessage", "id": "final-item", "text": "cleaned fixture", "phase": "final_answer"},
		}})
		_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{
			"threadId": "preprocess-thread", "turn": map[string]any{"id": turnID, "items": []any{}, "itemsView": "summary", "status": "completed", "error": nil},
		}})
	}
}

func runResumeAdapterFixture() {
	if os.Getenv("BRIA_PREPROCESS_SESSION") != "1" || os.Getenv("BRIA_START_MODE") != "resume" ||
		os.Getenv("BRIA_PROVIDER_SESSION_ID") != os.Getenv("PREPROCESS_EXPECT_RESUME_THREAD_ID") {
		os.Exit(44)
	}
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	threadID := os.Getenv("PREPROCESS_EXPECT_RESUME_THREAD_ID")
	_ = encoder.Encode(map[string]any{
		"protocol": 1, "type": "ready", "provider_session_id": threadID,
		"readiness": "protocol", "authentication": "unknown",
	})
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var request map[string]any
		if json.Unmarshal(line, &request) != nil {
			os.Exit(45)
		}
		if request["type"] == "close" {
			return
		}
	}
}

func runHangingCloseAdapterFixture() {
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	_ = encoder.Encode(map[string]any{
		"protocol": 1, "type": "ready", "provider_session_id": "technical-thread", "readiness": "protocol", "authentication": "unknown",
	})
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var request map[string]any
	if json.Unmarshal(line, &request) != nil {
		return
	}
	requestID, _ := request["request_id"].(string)
	messageID, _ := request["message_id"].(string)
	_ = encoder.Encode(map[string]any{"protocol": 1, "type": "accepted", "request_id": requestID, "message_id": messageID})
	_ = encoder.Encode(map[string]any{"protocol": 1, "type": "final", "request_id": requestID, "text": "cleaned fixture"})
	_ = encoder.Encode(map[string]any{"protocol": 1, "type": "completed", "request_id": requestID, "status": "completed"})
	_, _ = reader.ReadBytes('\n')
	<-time.After(time.Hour)
}
