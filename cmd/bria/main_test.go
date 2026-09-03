package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/safelog"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegramnotify"
)

func TestRunHelp(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "no arguments"},
		{name: "help command", args: []string{"help"}},
		{name: "help flag", args: []string{"--help"}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr strings.Builder
			if code := run(tc.args, &stdout, &stderr); code != 0 {
				t.Fatalf("run() exit code = %d, want 0", code)
			}
			if got := stderr.String(); got != "" {
				t.Errorf("stderr = %q, want empty", got)
			}

			got := stdout.String()
			for _, required := range []string{
				"Codex and Claude Telegram controller",
				"/status",
				"/new",
				"/sessions",
				"/use",
				"/stop",
				"bria run --config /absolute/path/to/config.json",
				"bria check-config --config /absolute/path/to/config.json",
				"bria check-telegram --config /absolute/path/to/config.json",
			} {
				if !strings.Contains(got, required) {
					t.Errorf("stdout = %q, want it to contain %q", got, required)
				}
			}
			if strings.Contains(got, "not implemented") || strings.Contains(got, "scaffold") {
				t.Errorf("stdout contains stale runtime claim: %q", got)
			}
			if strings.Contains(got, "persisted sessions are listed after restart") {
				t.Errorf("stdout claims recovered sessions are not resumed: %q", got)
			}
			if !strings.Contains(got, "OAuth/subscription login is not supported") ||
				!strings.Contains(got, "live provider authorization smoke test has not been run") {
				t.Errorf("stdout omits the current provider authorization limitations: %q", got)
			}
		})
	}
}

func TestRunVersion(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"version"}, {"--version"}} {
		var stdout, stderr strings.Builder
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Errorf("run(%q) exit code = %d, want 0", args, code)
		}
		if got, want := stdout.String(), "bria dev\n"; got != want {
			t.Errorf("run(%q) stdout = %q, want %q", args, got, want)
		}
		if got := stderr.String(); got != "" {
			t.Errorf("run(%q) stderr = %q, want empty", args, got)
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	t.Parallel()

	const secret = "sentinel-secret-must-not-reach-stderr"
	var stdout, stderr strings.Builder
	if code := run([]string{"start", "--token", secret}, &stdout, &stderr); code != 2 {
		t.Errorf("run() exit code = %d, want 2", code)
	}
	if got := stdout.String(); got != "" {
		t.Errorf("stdout = %q, want empty", got)
	}
	if got := stderr.String(); got != "bria: unknown command\n" {
		t.Errorf("stderr = %q, want generic unknown-command explanation", got)
	} else if strings.Contains(got, secret) || strings.Contains(got, "--token") || strings.Contains(got, "start") {
		t.Errorf("stderr echoes untrusted argv: %q", got)
	}
}

func TestRunRequiresAbsoluteConfigPath(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := run([]string{"run", "--config", "relative.json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run() exit code = %d, want 2", code)
	}
	if got := stdout.String(); got != "" {
		t.Errorf("stdout = %q, want empty", got)
	}
	if got := stderr.String(); !strings.Contains(got, "configuration path must be absolute") {
		t.Errorf("stderr = %q, want absolute-path explanation", got)
	}
}

func TestCheckConfigIsOfflineAndDoesNotAcquireInstanceOrCreateState(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:offline-secret")
	dependencies := testCommandDependencies(t, nil)
	dependencies.acquireLock = func(string) (instanceLock, error) {
		t.Fatal("check-config acquired the runtime instance lock")
		return nil, nil
	}
	dependencies.telegramHTTP = func() telegram.HTTPClient {
		t.Fatal("check-config constructed a Telegram transport")
		return nil
	}

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(context.Background(), []string{"check-config", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("check-config exit code = %d, stderr = %q", code, stderr.String())
	}
	if got, want := stdout.String(), "Bria configuration: OK\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertRuntimeFilesAbsent(t, statePath)
}

func TestVersionedExecutorCheckIsOfflineAndRunFailsBeforeTelegramOrLock(t *testing.T) {
	configPath := writeVersionedRoleConfig(t, t.TempDir(), config.RoleExecutor)
	dependencies := testCommandDependencies(t, nil)
	dependencies.acquireLock = func(string) (instanceLock, error) {
		t.Fatal("unsupported executor acquired local coordinator lock")
		return nil, nil
	}
	dependencies.telegramHTTP = func() telegram.HTTPClient {
		t.Fatal("executor constructed Telegram transport")
		return nil
	}

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(context.Background(), []string{"check-config", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("executor check-config exit code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runContextWithDependencies(context.Background(), []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 1 {
		t.Fatalf("executor run exit code = %d, want explicit unsupported failure", code)
	}
	if got := stderr.String(); !strings.Contains(got, "executor runtime is not connected") || strings.Contains(got, "Telegram") {
		t.Fatalf("executor run stderr = %q", got)
	}
}

func TestVersionedCoordinatorAndNetworkedCombinedFailBeforeLocalController(t *testing.T) {
	for _, role := range []config.Role{config.RoleCoordinator, config.RoleCombined} {
		t.Run(string(role), func(t *testing.T) {
			configPath := writeVersionedRoleConfig(t, t.TempDir(), role)
			dependencies := testCommandDependencies(t, nil)
			dependencies.acquireLock = func(string) (instanceLock, error) {
				t.Fatal("unsupported network role acquired local coordinator lock")
				return nil, nil
			}
			dependencies.telegramHTTP = func() telegram.HTTPClient {
				t.Fatal("unsupported network role constructed Telegram transport")
				return nil
			}
			var stdout, stderr strings.Builder
			if code := runContextWithDependencies(context.Background(), []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 1 {
				t.Fatalf("run exit code = %d, want fail-closed network role", code)
			}
			if !strings.Contains(stderr.String(), "network role runtime is not connected") {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestCheckTelegramPerformsOnlyIdentityProbeWithoutStateOrLock(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:identity-secret")
	dependencies := testCommandDependencies(t, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/bot123:identity-secret/getMe" {
			t.Fatalf("check-telegram request path = %q, want getMe only", request.URL.Path)
		}
		assertRuntimeFilesAbsent(t, statePath)
		return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
	})})
	dependencies.acquireLock = func(string) (instanceLock, error) {
		t.Fatal("check-telegram acquired the runtime instance lock")
		return nil, nil
	}

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(context.Background(), []string{"check-telegram", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("check-telegram exit code = %d, stderr = %q", code, stderr.String())
	}
	if got, want := stdout.String(), "Telegram identity: OK\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertRuntimeFilesAbsent(t, statePath)
}

func TestRunRetriesTransientIdentityFailureWhileCheckTelegramRemainsOneShot(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:readiness-retry-secret")

	checkCalls := 0
	checkDependencies := testCommandDependencies(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		checkCalls++
		return nil, errors.New("transient identity failure")
	})})
	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(context.Background(), []string{"check-telegram", "--config", configPath}, &stdout, &stderr, checkDependencies); code != 1 {
		t.Fatalf("check-telegram exit code = %d, want 1", code)
	}
	if checkCalls != 1 {
		t.Fatalf("check-telegram calls = %d, want one-shot identity probe", checkCalls)
	}

	runCalls := 0
	runDependencies := testCommandDependencies(t, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		runCalls++
		switch runCalls {
		case 1:
			if request.URL.Path != "/bot123:readiness-retry-secret/getMe" {
				t.Fatalf("first run request path = %q, want getMe", request.URL.Path)
			}
			return nil, errors.New("transient identity failure")
		case 2:
			if request.URL.Path != "/bot123:readiness-retry-secret/getMe" {
				t.Fatalf("second run request path = %q, want retried getMe", request.URL.Path)
			}
			return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
		case 3:
			if request.URL.Path != "/bot123:readiness-retry-secret/getUpdates" {
				t.Fatalf("third run request path = %q, want bootstrap getUpdates", request.URL.Path)
			}
			return telegramResponse(`{"ok":true,"result":[]}`), nil
		case 4:
			if request.URL.Path != "/bot123:readiness-retry-secret/getUpdates" {
				t.Fatalf("fourth run request path = %q, want live getUpdates", request.URL.Path)
			}
			return telegramResponse(`{"ok":false,"error_code":409,"description":"conflict"}`), nil
		default:
			t.Fatalf("unexpected run Telegram request %d", runCalls)
			return nil, nil
		}
	})})
	stdout.Reset()
	stderr.Reset()
	if code := runContextWithDependencies(context.Background(), []string{"run", "--config", configPath}, &stdout, &stderr, runDependencies); code != 1 {
		t.Fatalf("run exit code = %d, want 1 after non-transient poll conflict", code)
	}
	if runCalls != 4 {
		t.Fatalf("run Telegram calls = %d, want identity retry followed by bootstrap and poll", runCalls)
	}
	if got := stderr.String(); !strings.Contains(got, "error_code=409") || strings.Contains(got, "readiness-retry-secret") {
		t.Fatalf("run stderr = %q, want redacted typed poll conflict", got)
	}
	assertRedactedControllerStop(t, statePath)
}

func TestRunReadsCallbackKeyBeforeNetworkOrState(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:key-preflight-secret")
	if err := os.WriteFile(filepath.Join(temporary, "callback-key"), []byte("too-short"), 0o600); err != nil {
		t.Fatalf("replace callback key: %v", err)
	}
	calls := 0
	dependencies := testCommandDependencies(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("network must not be reached")
	})})

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(context.Background(), []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 1 {
		t.Fatalf("run exit code = %d, want 1", code)
	}
	if calls != 0 {
		t.Fatalf("Telegram calls = %d, want 0 before callback-key preflight", calls)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("state exists after callback-key preflight failure: %v", err)
	}
	if strings.Contains(stderr.String(), "key-preflight-secret") || strings.Contains(stderr.String(), "too-short") {
		t.Fatalf("stderr leaked secret material: %q", stderr.String())
	}
}

func TestRunStartsControlPlaneWithNoEnabledProviders(t *testing.T) {
	temporary := t.TempDir()
	configPath, _ := writeStatusConfig(t, temporary, "123:no-provider-secret")
	disableAllProviders(t, configPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	dependencies := testCommandDependencies(t, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if request.URL.Path != "/bot123:no-provider-secret/getMe" {
				t.Fatalf("request 1 path = %q, want getMe", request.URL.Path)
			}
			return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
		case 2:
			return telegramResponse(`{"ok":true,"result":[]}`), nil
		case 3:
			return telegramResponse(`{"ok":true,"result":[{"update_id":11,"message":{"message_id":12,"from":{"id":42,"is_bot":false,"first_name":"A"},"chat":{"id":42,"type":"private"},"text":"/status"}}]}`), nil
		case 4:
			if request.URL.Path != "/bot123:no-provider-secret/sendMessage" {
				t.Fatalf("request 4 path = %q, want sendMessage", request.URL.Path)
			}
			cancel()
			return telegramResponse(`{"ok":true,"result":{"message_id":13,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"No active session"}}`), nil
		default:
			t.Fatalf("unexpected request %d to %s", calls, request.URL.Redacted())
			return nil, nil
		}
	})})
	briaPath, err := dependencies.executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"codex", "claude"} {
		adapterPath := filepath.Join(filepath.Dir(briaPath), "bria-"+provider+"-adapter")
		if err := os.Remove(adapterPath); err != nil {
			t.Fatalf("remove disabled %s adapter fixture: %v", provider, err)
		}
	}

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(ctx, []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("run exit code = %d, stderr = %q", code, stderr.String())
	}
	if calls != 4 {
		t.Fatalf("HTTP calls = %d, want control-plane status flow without provider startup", calls)
	}
}

func TestRunAppliesEffectiveSessionLifetimeToCreatedSession(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:lifetime-secret")
	preferenceStore, err := settings.OpenFileStore(statePath + ".settings.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := preferenceStore.Update(context.Background(), func(current *settings.Settings) error {
		current.SessionLifetime = settings.Lifetime6Hours
		current.QueueLimit = 7
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	dependencies := testCommandDependencies(t, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
		case 2:
			return telegramResponse(`{"ok":true,"result":[]}`), nil
		case 3:
			return telegramResponse(fmt.Sprintf(`{"ok":true,"result":[{"update_id":21,"message":{"message_id":22,"from":{"id":42,"is_bot":false,"first_name":"A"},"chat":{"id":42,"type":"private"},"text":%q}}]}`, "/new codex "+temporary)), nil
		case 4:
			cancel()
			return telegramResponse(`{"ok":true,"result":{"message_id":23,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"session"}}`), nil
		default:
			t.Fatalf("unexpected request %d to %s", calls, request.URL.Redacted())
			return nil, nil
		}
	})})
	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(ctx, []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("run exit code = %d, stderr = %q", code, stderr.String())
	}

	state, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := state.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	if got, want := sessions[0].Lifetime(), domain.SessionLifetime6Hours; got != want {
		t.Fatalf("created session lifetime = %s, want %s", time.Duration(got), time.Duration(want))
	}
}

func TestRunClosesAnExpiredRecoveredSessionDuringStartup(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:expiry-secret")
	logDirectory := statePath + ".logs"
	if err := os.Mkdir(logDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(logDirectory, "detailed.jsonl"),
		[]byte("{\"class\":\"detailed\",\"type\":\"stale\",\"time\":\"2000-01-01T00:00:00Z\"}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	state, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC().Add(-7 * time.Hour)
	starting, err := domain.NewStartingSessionAt(
		"00000000-0000-4000-8000-000000000091",
		"expiry-intent",
		"local",
		domain.ProviderCodex,
		temporary,
		createdAt,
		domain.SessionLifetime6Hours,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := state.PutStartingIfAbsent(context.Background(), starting); err != nil || !inserted {
		t.Fatalf("persist starting session = inserted %t, error %v", inserted, err)
	}
	ready, err := starting.ReadyAt(domain.ProviderBinding{
		Provider: domain.ProviderCodex, SessionID: "expired-provider-session", Generation: 1,
	}, createdAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.CompareAndSwap(context.Background(), starting, ready); err != nil {
		t.Fatal(err)
	}

	runtime := &expiryProviderRuntime{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	dependencies := testCommandDependencies(t, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
		case 2:
			return telegramResponse(`{"ok":true,"result":[]}`), nil
		case 3:
			deadline := time.Now().Add(time.Second)
			for {
				persisted, loadErr := state.Load(context.Background(), ready.ID())
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				if persisted.Status() == domain.SessionArchived {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("expired startup session status = %q, want archived", persisted.Status())
				}
				time.Sleep(time.Millisecond)
			}
			cancel()
			return nil, context.Canceled
		default:
			t.Fatalf("unexpected request %d to %s", calls, request.URL.Redacted())
			return nil, nil
		}
	})})
	dependencies.composeRuntime = func(config.Config, []string, string, sessionruntime.Options) (providerRuntime, error) {
		return runtime, nil
	}

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(ctx, []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("run exit code = %d, stderr = %q", code, stderr.String())
	}
	if runtime.startCalls != 1 || runtime.abortCalls != 1 {
		t.Fatalf("runtime calls = start %d, abort %d, want one exact recovery then one confirmed close", runtime.startCalls, runtime.abortCalls)
	}
	persistedLog, err := os.ReadFile(filepath.Join(logDirectory, "detailed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(persistedLog) != 0 {
		t.Fatalf("expired detailed log survived startup cleanup: %s", persistedLog)
	}
}

func TestRunAppliesEffectiveQueueLimitToController(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:queue-secret")
	preferenceStore, err := settings.OpenFileStore(statePath + ".settings.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := preferenceStore.Update(context.Background(), func(current *settings.Settings) error {
		current.QueueLimit = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	runtime := &blockingProviderRuntime{entered: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	dependencies := testCommandDependencies(t, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
		case 2:
			return telegramResponse(`{"ok":true,"result":[]}`), nil
		case 3:
			return telegramResponse(fmt.Sprintf(`{"ok":true,"result":[{"update_id":31,"message":{"message_id":32,"from":{"id":42,"is_bot":false,"first_name":"A"},"chat":{"id":42,"type":"private"},"text":%q}}]}`, "/new codex "+temporary)), nil
		case 4:
			return telegramResponse(`{"ok":true,"result":{"message_id":33,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"session"}}`), nil
		case 5:
			return telegramResponse(`{"ok":true,"result":[{"update_id":32,"message":{"message_id":34,"from":{"id":42,"is_bot":false,"first_name":"A"},"chat":{"id":42,"type":"private"},"text":"first"}}]}`), nil
		case 6:
			select {
			case <-runtime.entered:
			case <-time.After(time.Second):
				t.Fatal("configured runtime did not start the first turn")
			}
			state, err := storage.OpenSessionStore(statePath)
			if err != nil {
				t.Fatal(err)
			}
			sessions, err := state.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(sessions) != 1 || sessions[0].Status() != domain.SessionRunning {
				t.Fatalf("durable turn state = %#v, want one running session", sessions)
			}
			return telegramResponse(`{"ok":true,"result":{"message_id":35,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"accepted"}}`), nil
		case 7:
			return telegramResponse(`{"ok":true,"result":[{"update_id":33,"message":{"message_id":36,"from":{"id":42,"is_bot":false,"first_name":"A"},"chat":{"id":42,"type":"private"},"text":"second"}}]}`), nil
		case 8:
			return telegramResponse(`{"ok":true,"result":{"message_id":37,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"accepted"}}`), nil
		case 9:
			return telegramResponse(`{"ok":true,"result":[{"update_id":34,"message":{"message_id":38,"from":{"id":42,"is_bot":false,"first_name":"A"},"chat":{"id":42,"type":"private"},"text":"third"}}]}`), nil
		case 10:
			body := requestBody(t, request)
			if !strings.Contains(body, "Не удалось надёжно сохранить запрос") || !strings.Contains(body, "не принят") {
				t.Fatalf("third turn response = %q, want effective queue-limit rejection", body)
			}
			cancel()
			return telegramResponse(`{"ok":true,"result":{"message_id":39,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"full"}}`), nil
		default:
			if ctx.Err() != nil && request.URL.Path == "/bot123:queue-secret/sendMessage" {
				return telegramResponse(`{"ok":true,"result":{"message_id":40,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"closing"}}`), nil
			}
			t.Fatalf("unexpected request %d to %s", calls, request.URL.Redacted())
			return nil, nil
		}
	})})
	dependencies.composeRuntime = func(config.Config, []string, string, sessionruntime.Options) (providerRuntime, error) {
		return runtime, nil
	}
	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(ctx, []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("run exit code = %d, stderr = %q", code, stderr.String())
	}
}

func TestLoadEffectiveSettingsRetainsLastGoodAfterInvalidLocalEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(current *settings.Settings) error {
		current.QueueLimit = 41
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"revision":2,"queue_limit":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	effective, err := loadEffectiveSettings(context.Background(), store)
	if err != nil {
		t.Fatalf("loadEffectiveSettings() error = %v, want last-good settings", err)
	}
	if effective.QueueLimit != 41 {
		t.Fatalf("effective queue limit = %d, want last-good 41", effective.QueueLimit)
	}
	if store.LastReloadError() == nil {
		t.Fatal("LastReloadError() = nil, want invalid edit observable")
	}
}

func TestConfiguredComputerIDUsesVersionedStableIdentity(t *testing.T) {
	legacyID, err := configuredComputerID(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if legacyID != "local" {
		t.Fatalf("legacy computer ID = %q, want local", legacyID)
	}
	versionedID, err := configuredComputerID(config.Config{
		Version:  config.CurrentVersion,
		Computer: &config.ComputerConfig{ID: "artem-mac", Name: "Artem Mac"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if versionedID != "artem-mac" {
		t.Fatalf("versioned computer ID = %q, want artem-mac", versionedID)
	}
	if _, err := configuredComputerID(config.Config{Version: config.CurrentVersion}); err == nil {
		t.Fatal("versioned config without computer identity was accepted")
	}
}

func TestReplyRouteRecorderMakesConfirmedNotificationReplyResolvable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reply-routes.json")
	store, err := storage.OpenTelegramReplyRouteStore(path, 42, 42)
	if err != nil {
		t.Fatal(err)
	}
	recorder := replyRouteRecorder{store: store}
	want := domain.SessionID("00000000-0000-4000-8000-000000000001")
	if err := recorder.RecordOutboundReceipt(context.Background(), telegramnotify.OutboundReceipt{
		MessageID: 91,
		SessionID: want,
	}); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.ResolveReply(context.Background(), 91)
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != want {
		t.Fatalf("ResolveReply(91) = (%q, %t), want (%q, true)", got, found, want)
	}
}

func TestRunStatusFlowQuarantinesBacklogPersistsReceiptAndDoesNotReplay(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:status-flow-secret")

	originalTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = originalTransport }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &statusFlowTransport{t: t, statePath: statePath, cancel: cancel}
	http.DefaultTransport = transport
	dependencies := testCommandDependencies(t, telegram.NewProductionHTTPClient())

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(ctx, []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("first run exit code = %d, want graceful cancellation after completed flow", code)
	}
	if transport.sendCalls != 2 {
		t.Fatalf("sendMessage calls = %d, want 2", transport.sendCalls)
	}
	if !transport.signedKeyboard || transport.unsignedKeyboard {
		t.Fatalf("/status keyboard signed=%t unsigned=%t, want signed-only callbacks", transport.signedKeyboard, transport.unsignedKeyboard)
	}
	if strings.Contains(stderr.String(), "status-flow-secret") {
		t.Fatalf("stderr leaked Telegram token: %q", stderr.String())
	}

	store, err := storage.OpenCoordinatorCheckpointStore(statePath)
	if err != nil {
		t.Fatalf("OpenCoordinatorCheckpointStore() error = %v", err)
	}
	checkpoint, found, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !found {
		t.Fatal("durable checkpoint not found")
	}
	if checkpoint.Checkpoint.NextUpdateID != 81 || checkpoint.Checkpoint.Blocked != nil {
		t.Fatalf("checkpoint = %#v, want confirmed offset 81 without a block", checkpoint)
	}
	if checkpoint.Checkpoint.Outbound == nil || checkpoint.Checkpoint.Outbound.Receipt == nil ||
		checkpoint.Checkpoint.Outbound.Receipt.MessageID != 902 {
		if checkpoint.Checkpoint.Outbound != nil {
			t.Fatalf("checkpoint outbound = %#v, want durable receipt 902", *checkpoint.Checkpoint.Outbound)
		}
		t.Fatalf("checkpoint = %#v, want durable receipt 902", checkpoint)
	}

	transport.restart = true
	restartContext, cancelRestart := context.WithCancel(context.Background())
	defer cancelRestart()
	transport.cancel = cancelRestart
	stdout.Reset()
	stderr.Reset()
	if code := runContextWithDependencies(restartContext, []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("restart exit code = %d, want graceful cancellation", code)
	}
	if transport.sendCalls != 2 {
		t.Fatalf("sendMessage calls after restart = %d, want no replay", transport.sendCalls)
	}
	if transport.restartCalls != 2 {
		t.Fatalf("restart HTTP calls = %d, want identity preflight and resumed poll only", transport.restartCalls)
	}
}

func TestRunRejectsWrongBotBeforeBacklogQuarantine(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:wrong-bot-secret")

	originalTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = originalTransport }()
	calls := 0
	http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Path != "/bot123:wrong-bot-secret/getMe" {
			t.Fatalf("request path = %q, want getMe only", request.URL.Path)
		}
		return telegramResponse(`{"ok":true,"result":{"id":700,"is_bot":true,"first_name":"Other","username":"other_bot"}}`), nil
	})
	dependencies := testCommandDependencies(t, telegram.NewProductionHTTPClient())

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(context.Background(), []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 1 {
		t.Fatalf("run() exit code = %d, want 1", code)
	}
	if calls != 1 {
		t.Fatalf("Telegram calls = %d, want one identity check and no bootstrap", calls)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("state exists after identity mismatch: %v", err)
	}
	if got := stderr.String(); strings.Contains(got, "wrong-bot-secret") || !strings.Contains(got, "does not match") {
		t.Fatalf("stderr = %q, want safe identity mismatch", got)
	}
}

func TestRunRejectsWrongBotBeforeRecoveringProviderProcesses(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:wrong-bot-recovery-secret")
	state, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	starting, err := domain.NewStartingSession(
		"00000000-0000-4000-8000-000000000092",
		"wrong-bot-recovery-intent",
		"local",
		domain.ProviderCodex,
		temporary,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := state.PutStartingIfAbsent(context.Background(), starting); err != nil || !inserted {
		t.Fatalf("persist starting session = inserted %t, error %v", inserted, err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{
		Provider: domain.ProviderCodex, SessionID: "must-not-resume", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.CompareAndSwap(context.Background(), starting, ready); err != nil {
		t.Fatal(err)
	}

	runtime := &expiryProviderRuntime{}
	dependencies := testCommandDependencies(t, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/bot123:wrong-bot-recovery-secret/getMe" {
			t.Fatalf("request path = %q, want getMe only", request.URL.Path)
		}
		return telegramResponse(`{"ok":true,"result":{"id":700,"is_bot":true,"first_name":"Other","username":"other_bot"}}`), nil
	})})
	dependencies.composeRuntime = func(config.Config, []string, string, sessionruntime.Options) (providerRuntime, error) {
		return runtime, nil
	}

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(context.Background(), []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 1 {
		t.Fatalf("run exit code = %d, stderr = %q", code, stderr.String())
	}
	if runtime.startCalls != 0 || runtime.abortCalls != 0 {
		t.Fatalf("wrong Telegram identity caused provider calls: start %d, abort %d", runtime.startCalls, runtime.abortCalls)
	}
	persisted, err := state.Load(context.Background(), ready.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Equal(ready) {
		t.Fatalf("wrong Telegram identity mutated provider session: %#v", persisted)
	}
}

func TestRunDoesNotRecoverAnAcceptedTurnWithoutDurableReconciliation(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:reconciliation-secret")
	state, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	starting, err := domain.NewStartingSession(
		"00000000-0000-4000-8000-000000000093",
		"accepted-turn-intent",
		"local",
		domain.ProviderCodex,
		temporary,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := state.PutStartingIfAbsent(context.Background(), starting); err != nil || !inserted {
		t.Fatalf("persist starting session = inserted %t, error %v", inserted, err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{
		Provider: domain.ProviderCodex, SessionID: "accepted-turn-provider", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.CompareAndSwap(context.Background(), starting, ready); err != nil {
		t.Fatal(err)
	}
	running, err := ready.StartWork(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Replace(context.Background(), ready, running); err != nil {
		t.Fatal(err)
	}

	runtime := &expiryProviderRuntime{}
	calls := 0
	dependencies := testCommandDependencies(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
	})})
	dependencies.composeRuntime = func(config.Config, []string, string, sessionruntime.Options) (providerRuntime, error) {
		return runtime, nil
	}

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(context.Background(), []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 1 {
		t.Fatalf("run exit code = %d, want fail-closed startup; stderr = %q", code, stderr.String())
	}
	if runtime.startCalls != 0 || runtime.abortCalls != 0 {
		t.Fatalf("unreconciled accepted turn caused provider calls: start %d, abort %d", runtime.startCalls, runtime.abortCalls)
	}
	if calls != 1 {
		t.Fatalf("Telegram calls = %d, want identity gate only", calls)
	}
	persisted, err := state.Load(context.Background(), running.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Equal(running) {
		t.Fatalf("unreconciled accepted turn mutated on startup: %#v", persisted)
	}
	if !strings.Contains(stderr.String(), "accepted turn reconciliation required") {
		t.Fatalf("stderr = %q, want explicit reconciliation blocker", stderr.String())
	}
}

func TestRunNeverExecutesUnsignedRawCallbackData(t *testing.T) {
	temporary := t.TempDir()
	configPath, _ := writeStatusConfig(t, temporary, "123:unsigned-callback-secret")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	mutationPath := ""
	dependencies := testCommandDependencies(t, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
		case 2:
			return telegramResponse(`{"ok":true,"result":[]}`), nil
		case 3:
			return telegramResponse(`{"ok":true,"result":[{"update_id":51,"callback_query":{"id":"callback-51","from":{"id":42,"is_bot":false,"first_name":"A"},"message":{"message_id":52,"from":{"id":600,"is_bot":true,"first_name":"Bria"},"chat":{"id":42,"type":"private"}},"data":"ft:stop"}}]}`), nil
		case 4:
			if request.URL.Path != "/bot123:unsigned-callback-secret/answerCallbackQuery" {
				mutationPath = request.URL.Path
				return telegramResponse(`{"ok":false,"error_code":400,"description":"unsafe callback mutation rejected"}`), nil
			}
			if body := requestBody(t, request); !strings.Contains(body, `"callback_query_id":"callback-51"`) {
				t.Fatalf("answerCallbackQuery body = %q", body)
			}
			return telegramResponse(`{"ok":true,"result":true}`), nil
		case 5:
			if request.URL.Path != "/bot123:unsigned-callback-secret/sendMessage" {
				mutationPath = request.URL.Path
				return telegramResponse(`{"ok":false,"error_code":400,"description":"unsafe callback mutation rejected"}`), nil
			}
			body := requestBody(t, request)
			if !strings.Contains(body, "недействительна") || strings.Contains(body, "reply_markup") {
				t.Fatalf("stale callback response = %q", body)
			}
			return telegramResponse(`{"ok":true,"result":{"message_id":53,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"stale"}}`), nil
		case 6:
			if request.URL.Path != "/bot123:unsigned-callback-secret/getUpdates" {
				mutationPath = request.URL.Path
				return telegramResponse(`{"ok":false,"error_code":400,"description":"unsafe callback mutation rejected"}`), nil
			}
			cancel()
			return nil, context.Canceled
		default:
			t.Fatalf("unexpected request %d to %s", calls, request.URL.Redacted())
			return nil, nil
		}
	})})

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(ctx, []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("run exit code = %d, mutation = %q, stderr = %q", code, mutationPath, stderr.String())
	}
	if calls != 6 {
		t.Fatalf("Telegram calls = %d, want identity, bootstrap, callback poll, safe stale response, resumed poll", calls)
	}
	if mutationPath != "" {
		t.Fatalf("raw callback reached unsafe mutation endpoint %q", mutationPath)
	}
}

func TestRunRedactsTelegramAPIFailure(t *testing.T) {
	temporary := t.TempDir()
	configPath, _ := writeStatusConfig(t, temporary, "123:redaction-secret")

	originalTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = originalTransport }()
	http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return telegramResponse(fmt.Sprintf(
			`{"ok":false,"error_code":401,"description":"request %s failed: raw-api-marker"}`,
			request.URL.String(),
		)), nil
	})
	dependencies := testCommandDependencies(t, telegram.NewProductionHTTPClient())

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(context.Background(), []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 1 {
		t.Fatalf("run() exit code = %d, want 1", code)
	}
	got := stderr.String()
	for _, forbidden := range []string{"redaction-secret", "api.telegram.org", "raw-api-marker"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("stderr leaked %q: %q", forbidden, got)
		}
	}
}

func assertRedactedControllerStop(t *testing.T, statePath string) {
	t.Helper()
	safeLogger, err := safelog.Open(safelog.Options{Directory: statePath + ".logs"})
	if err != nil {
		t.Fatal(err)
	}
	records, err := safeLogger.Read(safelog.Critical)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Type != "controller.stopped" || records[0].Error != "[REDACTED]" {
		t.Fatalf("critical safe log = %#v, want one redacted controller stop", records)
	}
}

func TestRunContextCancellationIsGracefulAfterReadiness(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:cancellation-secret")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	originalTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = originalTransport }()
	calls := 0
	http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
		case 2:
			return telegramResponse(`{"ok":true,"result":[]}`), nil
		case 3:
			cancel()
			return nil, errors.New("raw cancellation transport detail")
		default:
			t.Fatalf("unexpected Telegram request %d: %s", calls, request.URL.Redacted())
			return nil, nil
		}
	})
	dependencies := testCommandDependencies(t, telegram.NewProductionHTTPClient())

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(ctx, []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("runContext() exit code = %d, want 0", code)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want graceful cancellation", got)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("bootstrap checkpoint missing before cancellation: %v", err)
	}
}

type statusFlowTransport struct {
	t                *testing.T
	statePath        string
	calls            int
	sendCalls        int
	restart          bool
	restartCalls     int
	unsignedKeyboard bool
	signedKeyboard   bool
	cancel           context.CancelFunc
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func (transport *statusFlowTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.t.Helper()
	if request.URL.Scheme != "https" || request.URL.Host != "api.telegram.org" {
		transport.t.Fatalf("Telegram destination = %s, want official TLS endpoint", request.URL.Redacted())
	}
	if transport.restart {
		transport.restartCalls++
		switch transport.restartCalls {
		case 1:
			if request.URL.Path != "/bot123:status-flow-secret/getMe" {
				transport.t.Fatalf("restart request path = %q, want getMe", request.URL.Path)
			}
			return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
		case 2:
			if request.URL.Path != "/bot123:status-flow-secret/getUpdates" {
				transport.t.Fatalf("restart request path = %q, want getUpdates", request.URL.Path)
			}
			transport.cancel()
			return nil, errors.New("stop resumed test poll")
		default:
			transport.t.Fatalf("unexpected restart request %d", transport.restartCalls)
			return nil, nil
		}
	}

	transport.calls++
	switch transport.calls {
	case 1:
		if request.URL.Path != "/bot123:status-flow-secret/getMe" {
			transport.t.Fatalf("first request path = %q, want getMe identity preflight", request.URL.Path)
		}
		if _, err := os.Stat(transport.statePath); !os.IsNotExist(err) {
			transport.t.Fatalf("state exists before identity preflight: %v", err)
		}
		lockPath := filepath.Join(filepath.Dir(transport.statePath), "."+filepath.Base(transport.statePath)+".lock")
		if info, err := os.Stat(lockPath); err != nil || !info.Mode().IsRegular() {
			transport.t.Fatalf("instance lock missing before identity preflight: %v", err)
		}
		return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
	case 2:
		if request.URL.Path != "/bot123:status-flow-secret/getUpdates" {
			transport.t.Fatalf("second request path = %q, want backlog quarantine", request.URL.Path)
		}
		if body := requestBody(transport.t, request); !strings.Contains(body, `"offset":-1`) {
			transport.t.Fatalf("bootstrap body = %q, want offset -1", body)
		}
		return telegramResponse(`{"ok":true,"result":[{"update_id":77,"message":{"message_id":1,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"stale must not run"}}]}`), nil
	case 3:
		if request.URL.Path != "/bot123:status-flow-secret/getUpdates" {
			transport.t.Fatalf("third request path = %q, want live poll after readiness", request.URL.Path)
		}
		state, err := os.ReadFile(transport.statePath)
		if err != nil {
			transport.t.Fatalf("read persisted bootstrap checkpoint: %v", err)
		}
		if !strings.Contains(string(state), `"next_update_id": 78`) {
			transport.t.Fatalf("state before live poll = %s, want persisted bootstrap fence 78", state)
		}
		if body := requestBody(transport.t, request); !strings.Contains(body, `"offset":78`) {
			transport.t.Fatalf("live poll body = %q, want offset 78", body)
		}
		return telegramResponse(`{"ok":true,"result":[
  {"update_id":78,"message":{"message_id":2,"from":{"id":7},"chat":{"id":7,"type":"private"},"text":"/status"}},
  {"update_id":79,"message":{"message_id":3,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/status"}},
  {"update_id":80,"message":{"message_id":4,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/future"}}
]}`), nil
	case 4:
		transport.sendCalls++
		if request.URL.Path != "/bot123:status-flow-secret/sendMessage" {
			transport.t.Fatalf("fourth request path = %q, want sendMessage", request.URL.Path)
		}
		body := requestBody(transport.t, request)
		if !strings.Contains(body, `"chat_id":42`) || strings.Contains(body, "stale must not run") {
			transport.t.Fatalf("sendMessage body = %q", body)
		}
		if strings.Contains(body, "menu:") || strings.Contains(body, "ft:") {
			transport.unsignedKeyboard = true
			return telegramResponse(`{"ok":false,"error_code":400,"description":"unsigned keyboard rejected"}`), nil
		}
		transport.signedKeyboard = strings.Contains(body, "reply_markup") && strings.Contains(body, "callback_data")
		if !transport.signedKeyboard {
			return telegramResponse(`{"ok":false,"error_code":400,"description":"signed keyboard required"}`), nil
		}
		return telegramResponse(`{"ok":true,"result":{"message_id":901,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"Bria works"}}`), nil
	case 5:
		transport.sendCalls++
		if request.URL.Path != "/bot123:status-flow-secret/sendMessage" {
			transport.t.Fatalf("fifth request path = %q, want sendMessage", request.URL.Path)
		}
		return telegramResponse(`{"ok":true,"result":{"message_id":902,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"No active session"}}`), nil
	case 6:
		if request.URL.Path != "/bot123:status-flow-secret/getUpdates" {
			transport.t.Fatalf("sixth request path = %q, want getUpdates", request.URL.Path)
		}
		transport.cancel()
		return nil, errors.New("stop initial test poll")
	default:
		transport.t.Fatalf("unexpected Telegram request %d: %s", transport.calls, request.URL.Redacted())
		return nil, nil
	}
}

func writePrivateFile(t *testing.T, directory, name, contents string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod %s: %v", name, err)
	}
	return path
}

func writeStatusConfig(t *testing.T, directory, token string) (string, string) {
	t.Helper()
	statePath := filepath.Join(directory, "state.json")
	tokenPath := writePrivateFile(t, directory, "telegram-token", token+"\n")
	callbackPath := writePrivateFile(t, directory, "callback-key", strings.Repeat("k", 32))
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	configPath := writePrivateFile(t, directory, "config.json", fmt.Sprintf(`{
  "owner_user_id": 42,
  "private_chat_id": 42,
  "bot_username": "my_bria_bot",
  "state_path": %q,
  "telegram_token": {"secret_file": %q},
  "callback_key": {"secret_file": %q},
  "providers": {
    "codex": {"enabled": true, "command": {"exec": %q, "argv": []}},
    "claude": {"enabled": false}
  }
}
`, statePath, tokenPath, callbackPath, executable))
	return configPath, statePath
}

func writeVersionedRoleConfig(t *testing.T, directory string, role config.Role) string {
	t.Helper()
	statePath := filepath.Join(directory, "state.json")
	coordinatorAddress := ""
	listenerAddress := "127.0.0.1:7443"
	pairingPath := filepath.Join(directory, "pairings.json")
	catalogPath := filepath.Join(directory, "catalog.json")
	fencePath := filepath.Join(directory, "fence.json")
	ledgerPath := filepath.Join(directory, "ledger.json")
	telegramFields := fmt.Sprintf(`
  "owner_user_id": 42,
  "private_chat_id": 42,
  "bot_username": "my_bria_bot",
  "telegram_token": {"secret_file": %q},
  "callback_key": {"secret_file": %q},`, filepath.Join(directory, "telegram-token"), filepath.Join(directory, "callback-key"))
	if role == config.RoleExecutor {
		coordinatorAddress = "coordinator.example:7443"
		listenerAddress = ""
		pairingPath = ""
		catalogPath = ""
		telegramFields = ""
	}
	if role == config.RoleCoordinator {
		fencePath = ""
		ledgerPath = ""
	}
	return writePrivateFile(t, directory, "versioned-"+string(role)+".json", fmt.Sprintf(`{
  "version": 1,
  "role": %q,
  "computer": {"id": "artem-mac", "name": "Artem Mac"},%s
  "state_path": %q,
  "network": {
    "coordinator_address": %q,
    "listener_address": %q,
    "certificate_file": %q,
    "private_key_file": %q,
    "trust_bundle_file": %q
  },
  "paths": {"pairing_path": %q, "catalog_path": %q, "fence_path": %q, "ledger_path": %q},
  "providers": {},
  "update": {"source_url": "https://updates.example/manifest.json", "trust_key_file": %q},
  "backup": {"destination": %q, "schedule": null, "encryption": null},
  "parakeet": {"executable": %q, "model_path": %q, "argv": ["{model_path}"]},
  "media_limits": {
    "download_bytes": 20971520, "upload_bytes": 52428800,
    "voice_bytes": 10485760, "photo_bytes": 20971520,
    "transcript_bytes": 1048576, "diagnostic_bytes": 65536
  }
}`, role, telegramFields, statePath, coordinatorAddress, listenerAddress,
		filepath.Join(directory, "tls.crt"), filepath.Join(directory, "tls.key"), filepath.Join(directory, "trust.pem"),
		pairingPath, catalogPath, fencePath, ledgerPath, filepath.Join(directory, "update.pub"),
		filepath.Join(directory, "backups"), filepath.Join(directory, "parakeet"), filepath.Join(directory, "parakeet-model.bin")))
}

func disableAllProviders(t *testing.T, configPath string) {
	t.Helper()
	document, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	document = []byte(strings.Replace(
		string(document),
		fmt.Sprintf(`"codex": {"enabled": true, "command": {"exec": %q, "argv": []}}`, executable),
		`"codex": {"enabled": false}`,
		1,
	))
	if err := os.WriteFile(configPath, document, 0o600); err != nil {
		t.Fatalf("write provider-disabled config: %v", err)
	}
}

func telegramResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func requestBody(t *testing.T, request *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return string(body)
}

func testCommandDependencies(t *testing.T, httpClient telegram.HTTPClient) commandDependencies {
	t.Helper()
	directory := t.TempDir()
	briaExecutable := filepath.Join(directory, "bria")
	for _, path := range []string{
		briaExecutable,
		filepath.Join(directory, "bria-codex-adapter"),
		filepath.Join(directory, "bria-claude-adapter"),
	} {
		if err := os.WriteFile(path, []byte("test executable"), 0o700); err != nil {
			t.Fatalf("write fake executable %q: %v", filepath.Base(path), err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatalf("chmod fake executable %q: %v", filepath.Base(path), err)
		}
	}
	if httpClient == nil {
		httpClient = telegram.NewProductionHTTPClient()
	}
	return commandDependencies{
		acquireLock: productionDependencies().acquireLock,
		executable: func() (string, error) {
			return briaExecutable, nil
		},
		environment: os.Environ,
		telegramHTTP: func() telegram.HTTPClient {
			return httpClient
		},
		composeRuntime: composeProviderRuntime,
	}
}

type blockingProviderRuntime struct {
	entered chan struct{}
}

type expiryProviderRuntime struct {
	startCalls int
	abortCalls int
}

func (runtime *expiryProviderRuntime) Start(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	runtime.startCalls++
	if request.Mode != app.SessionStartResume || request.PriorBinding == nil {
		return domain.ProviderBinding{}, errors.New("expiry fixture requires exact resume")
	}
	return domain.ProviderBinding{
		Provider: request.Provider, SessionID: request.PriorBinding.SessionID, Generation: request.PriorBinding.Generation + 1,
	}, nil
}

func (runtime *expiryProviderRuntime) Abort(_ context.Context, request app.StartSessionRequest, binding domain.ProviderBinding) error {
	runtime.abortCalls++
	if request.Mode != app.SessionStartResume || request.PriorBinding == nil ||
		binding.SessionID != request.PriorBinding.SessionID {
		return errors.New("expiry fixture received an inexact close")
	}
	return nil
}

func (*expiryProviderRuntime) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("expiry fixture does not submit turns")
}

func (runtime *blockingProviderRuntime) Start(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	return domain.ProviderBinding{Provider: request.Provider, SessionID: "provider-session", Generation: 1}, nil
}

func (*blockingProviderRuntime) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	return nil
}

func (runtime *blockingProviderRuntime) Submit(ctx context.Context, _ domain.SessionID, _ string) (sessionruntime.TurnResult, error) {
	select {
	case runtime.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return sessionruntime.TurnResult{}, ctx.Err()
}

func (runtime *blockingProviderRuntime) SubmitWithCallbacks(
	ctx context.Context,
	_ domain.SessionID,
	_ string,
	callbacks sessionruntime.TurnCallbacks,
) (sessionruntime.TurnResult, error) {
	if callbacks.OnAccepted == nil || callbacks.MessageID == "" {
		return sessionruntime.TurnResult{}, errors.New("durable acceptance callback is required")
	}
	if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
		return sessionruntime.TurnResult{}, err
	}
	select {
	case runtime.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return sessionruntime.TurnResult{}, ctx.Err()
}

func assertRuntimeFilesAbsent(t *testing.T, statePath string) {
	t.Helper()
	for _, path := range []string{
		statePath,
		filepath.Join(filepath.Dir(statePath), "."+filepath.Base(statePath)+".lock"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("runtime file %q exists: %v", filepath.Base(path), err)
		}
	}
}
