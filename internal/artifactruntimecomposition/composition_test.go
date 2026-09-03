package artifactruntimecomposition_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"bria/internal/artifactruntimecomposition"
	"bria/internal/callbacktoken"
	"bria/internal/config"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/turnprocessing"
)

type httpClientFunc func(*http.Request) (*http.Response, error)

func (function httpClientFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestOpenWithoutP4IsDisabledWithoutDependenciesOrWrites(t *testing.T) {
	bundle, enabled, err := artifactruntimecomposition.Open(artifactruntimecomposition.Options{Configuration: config.Config{}})
	if err != nil || enabled || bundle != nil {
		t.Fatalf("Open() = (%#v, %t, %v), want disabled nil bundle", bundle, enabled, err)
	}
}

func TestOpenReadsBoundedRetryKeyAndRoutesFinalIntoArtifactProduction(t *testing.T) {
	root := canonicalTemp(t)
	configuration, work := artifactConfiguration(t, root, writePrivate(t, root, "artifact-retry.key", bytes.Repeat([]byte{'k'}, 32)))
	artifactPath := filepath.Join(work, "result.txt")
	if err := os.WriteFile(artifactPath, []byte("artifact result"), 0o600); err != nil {
		t.Fatal(err)
	}

	documentCalls := 0
	client := testTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		documentCalls++
		if request.URL.Path != "/bot123:artifact-runtime/sendDocument" || request.Method != http.MethodPost {
			return nil, errors.New("unexpected Telegram request")
		}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			return nil, err
		}
		if request.FormValue("chat_id") != "42" {
			return nil, errors.New("artifact was not sent to the configured private chat")
		}
		file, header, err := request.FormFile("document")
		if err != nil {
			return nil, err
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil || header.Filename != "result.txt" || string(content) != "artifact result" {
			return nil, errors.New("unexpected artifact document")
		}
		return jsonResponse(`{"ok":true,"result":{"message_id":81,"from":{"id":600,"is_bot":true,"first_name":"Bria"},"chat":{"id":42,"type":"private"},"document":{"file_id":"artifact-file","file_unique_id":"artifact-unique","file_name":"result.txt","file_size":15}}}`), nil
	})

	bundle, enabled, err := artifactruntimecomposition.Open(artifactruntimecomposition.Options{Configuration: configuration, Telegram: client})
	if err != nil || !enabled || bundle == nil || bundle.Finals == nil {
		t.Fatalf("Open() = (%#v, %t, %v), want enabled final processor", bundle, enabled, err)
	}
	if err := bundle.Finals.ProcessFinal(context.Background(), turnprocessing.FinalObservation{
		SessionID: domain.SessionID("11111111-1111-4111-8111-111111111111"), MessageID: "telegram-update:9", OperationID: "telegram-update:9:final",
		Text: "[result](file://" + artifactPath + ")",
	}); err != nil {
		t.Fatalf("ProcessFinal() error = %v", err)
	}
	if documentCalls != 1 {
		t.Fatalf("artifact document calls = %d, want 1", documentCalls)
	}
	p4, ok := configuration.P4Runtime()
	if !ok {
		t.Fatal("P4Runtime() = disabled")
	}
	if info, err := os.Stat(p4.ArtifactRetryDirectory + ".bindings.json"); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("binding store = (%#v, %v), want deterministic private sibling", info, err)
	}
	if _, err := os.Stat(filepath.Join(p4.ArtifactRetryDirectory, ".bindings.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifact retry scan contains binding store: %v", err)
	}
}

func TestOpenFailsClosedForUnsafeRetryKeyWithoutCreatingArtifactState(t *testing.T) {
	for _, test := range []struct {
		name string
		key  func(*testing.T, string) string
	}{
		{name: "missing", key: func(_ *testing.T, root string) string { return filepath.Join(root, "missing.key") }},
		{name: "wrong mode", key: func(t *testing.T, root string) string {
			path := writePrivate(t, root, "mode.key", bytes.Repeat([]byte{'m'}, 32))
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "symlink", key: func(t *testing.T, root string) string {
			if runtime.GOOS == "windows" {
				t.Skip("symlink creation commonly requires privileges on Windows")
			}
			target := writePrivate(t, root, "target.key", bytes.Repeat([]byte{'s'}, 32))
			path := filepath.Join(root, "link.key")
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := canonicalTemp(t)
			configuration, _ := artifactConfiguration(t, root, test.key(t, root))
			p4, _ := configuration.P4Runtime()
			_, enabled, err := artifactruntimecomposition.Open(artifactruntimecomposition.Options{Configuration: configuration, Telegram: testTelegramClient(t, func(*http.Request) (*http.Response, error) {
				t.Fatal("unsafe retry key reached Telegram")
				return nil, nil
			})})
			if err == nil || !enabled {
				t.Fatalf("Open() = (enabled=%t, err=%v), want enabled configuration to fail closed", enabled, err)
			}
			for _, path := range []string{p4.ArtifactManifestDirectory, p4.ArtifactRetryDirectory, p4.ArtifactRetryDirectory + ".bindings.json"} {
				if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("unsafe key created %q: %v", path, statErr)
				}
			}
			if strings.Contains(err.Error(), p4.ArtifactRetryKey.SecretFile) {
				t.Fatalf("unsafe key error disclosed its path: %v", err)
			}
		})
	}
}

func TestBundleWrapsOnlyArtifactCallbacksAndBindsPublisherOnce(t *testing.T) {
	root := canonicalTemp(t)
	configuration, _ := artifactConfiguration(t, root, writePrivate(t, root, "artifact-retry.key", bytes.Repeat([]byte{'b'}, 32)))
	bundle, enabled, err := artifactruntimecomposition.Open(artifactruntimecomposition.Options{Configuration: configuration, Telegram: testTelegramClient(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("no artifact document is expected")
	})})
	if err != nil || !enabled || bundle == nil {
		t.Fatalf("Open() = (%#v, %t, %v)", bundle, enabled, err)
	}

	fallback := &callbackStub{}
	if _, err := bundle.WrapCallback(fallback).HandleCallback(context.Background(), telegrampipeline.CallbackPlan{Effect: telegrampipeline.EffectShowStatus}); err != nil {
		t.Fatalf("wrapped recovery callback error = %v", err)
	}
	if fallback.calls != 1 {
		t.Fatalf("non-artifact callback calls = %d, want preserved fallback", fallback.calls)
	}

	presenter := testPresenter(t)
	registry, err := telegrampipeline.OpenFileCallbackRegistry(filepath.Join(root, "callbacks.json"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := telegramflow.OpenFileCallbackOperationStore(filepath.Join(root, "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, sender, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 42, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: telegramstate.NewMemoryStore(), MessageUI: messageStub{}, Callbacks: bundle.WrapCallback(fallback), Operations: operations, Sender: statusStub{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.BindPublisher(presenter, sender); err != nil {
		t.Fatalf("BindPublisher() error = %v", err)
	}
	if err := bundle.BindPublisher(presenter, sender); err == nil {
		t.Fatal("second BindPublisher() succeeded")
	}
}

type callbackStub struct{ calls int }

func (stub *callbackStub) HandleCallback(context.Context, telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	stub.calls++
	return telegramflow.CallbackResult{}, nil
}

type messageStub struct{}

func (messageStub) HandleMessage(context.Context, coordinator.Update) (telegramflow.MessageResult, error) {
	return telegramflow.MessageResult{}, nil
}

type statusStub struct{}

func (statusStub) SendStatus(context.Context, string, coordinator.Status) (coordinator.Receipt, error) {
	return coordinator.Receipt{MessageID: 1}, nil
}

func (statusStub) SendStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	return coordinator.Receipt{MessageID: 1}, nil
}

func (statusStub) EditStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	return coordinator.Receipt{MessageID: 1}, nil
}

func testPresenter(t *testing.T) *telegrambridge.Presenter {
	t.Helper()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{'c'}, 32), bytes.NewReader(bytes.Repeat([]byte{'r'}, 512)), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, time.Now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return presenter
}

func testTelegramClient(t *testing.T, client httpClientFunc) *telegram.Client {
	t.Helper()
	result, err := telegram.NewClient("123:artifact-runtime", client, telegram.Options{MaxUploadBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func artifactConfiguration(t *testing.T, root, retryKey string) (config.Config, string) {
	t.Helper()
	path := func(name string) string { return filepath.Join(root, name) }
	work := path("artifact-work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"callback.key", "parakeet.model", "parakeet", "update.pub"} {
		mode := os.FileMode(0o600)
		if name == "parakeet" {
			mode = 0o700
		}
		writePrivateMode(t, root, name, []byte(name), mode)
	}
	legacy := config.Config{
		OwnerUserID: 42, PrivateChatID: 42, BotUsername: "bria_test_bot", StatePath: path("state.json"),
		TelegramToken: config.TelegramTokenRef{EnvVar: "BRIA_ARTIFACT_TEST_TOKEN"}, CallbackKey: config.CallbackKeyRef{SecretFile: path("callback.key")},
		Providers: map[string]config.ProviderConfig{"codex": {}, "claude": {}},
	}
	configuration, err := legacy.MigrateLegacy(config.LegacyMigration{
		Computer: config.ComputerConfig{ID: "artifact-test-computer", Name: "Artifact test computer"},
		Network:  config.NetworkConfig{}, Paths: config.RuntimePaths{PairingPath: path("pairing.json"), CatalogPath: path("catalog.json"), FencePath: path("fence.json"), LedgerPath: path("ledger.json")},
		Update: config.UpdateConfig{SourceURL: "https://updates.example/manifest.json", TrustKeyFile: path("update.pub")}, Backup: config.BackupConfig{Destination: path("backup")},
		Parakeet:    config.ParakeetConfig{Executable: path("parakeet"), ModelPath: path("parakeet.model"), Argv: []string{"{model_path}"}},
		MediaLimits: config.MediaLimits{DownloadBytes: 1024, UploadBytes: 1024, VoiceBytes: 512, PhotoBytes: 512, TranscriptBytes: 512, DiagnosticBytes: 256},
		Runtime: &config.RuntimeFeatures{P4: &config.P4RuntimeConfig{
			VoiceTempDirectory: path("voice-temp"), PhotoCustodyDirectory: path("photo-custody"),
			ArtifactManifestDirectory: path("artifact-manifests"), ArtifactRetryDirectory: path("artifact-retries"),
			ArtifactAllowedRoots: []string{work}, ArtifactRetryKey: config.SecretFileRef{SecretFile: retryKey}, ArtifactRetryTTLSeconds: 60,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return configuration, work
}

func writePrivate(t *testing.T, directory, name string, contents []byte) string {
	t.Helper()
	return writePrivateMode(t, directory, name, contents, 0o600)
}

func canonicalTemp(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writePrivateMode(t *testing.T, directory, name string, contents []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
