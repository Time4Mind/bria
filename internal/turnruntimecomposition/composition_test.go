package turnruntimecomposition_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/safelog"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/turnprocessing"
	"bria/internal/turnruntimecomposition"
)

type wiringHTTPClient func(*http.Request) (*http.Response, error)

func (function wiringHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

// This black-box constructor test proves the exact public assembly used by
// singlemachine keeps both P4 Finals and Prepared observability behavior.
func TestOpenWiresP4FinalsAndPreparedObservability(t *testing.T) {
	root := wiringCanonicalTemp(t)
	configuration, work := wiringConfiguration(t, root)
	artifactPath := filepath.Join(work, "final.txt")
	if err := os.WriteFile(artifactPath, []byte("final artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	documentCalls := 0
	client, err := telegram.NewClient("123:turnruntime-wiring", wiringHTTPClient(func(request *http.Request) (*http.Response, error) {
		documentCalls++
		if request.URL.Path != "/bot123:turnruntime-wiring/sendDocument" || request.Method != http.MethodPost {
			return nil, errors.New("unexpected Telegram request")
		}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			return nil, err
		}
		if request.FormValue("chat_id") != "42" {
			return nil, errors.New("artifact chat differs from configured private chat")
		}
		file, header, err := request.FormFile("document")
		if err != nil {
			return nil, err
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil || header.Filename != "final.txt" || string(content) != "final artifact" {
			return nil, errors.New("unexpected artifact bytes")
		}
		return wiringJSONResponse(`{"ok":true,"result":{"message_id":91,"from":{"id":600,"is_bot":true,"first_name":"Bria"},"chat":{"id":42,"type":"private"},"document":{"file_id":"final-file","file_unique_id":"final-unique","file_name":"final.txt","file_size":14}}}`), nil
	}), telegram.Options{MaxUploadBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := settings.OpenFileStore(filepath.Join(root, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := storage.OpenSessionStore(filepath.Join(root, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	logger, err := safelog.Open(safelog.Options{Directory: filepath.Join(root, "logs")})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &wiringRuntime{final: "provider final is not telemetry"}
	bundle, err := turnruntimecomposition.Open(turnruntimecomposition.Options{
		Configuration: configuration, Telegram: client, Settings: preferences, Sessions: sessions, Runtime: runtime, Logger: logger,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if bundle.Finals == nil {
		t.Fatal("P4 assembly did not receive artifact Finals")
	}
	prepared, ok := bundle.Submitter.(turnprocessing.PreparedTurnSubmitter)
	if !ok {
		t.Fatalf("instrumented submitter %T lost P4 prepared-input capability", bundle.Submitter)
	}

	const sessionID = domain.SessionID("11111111-1111-4111-8111-111111111111")
	session, err := domain.NewStartingSessionAt(sessionID, "wiring", "wiring-computer", domain.ProviderCodex, work, time.Now().UTC(), domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := sessions.PutStartingIfAbsent(context.Background(), session); err != nil || !inserted {
		t.Fatalf("PutStartingIfAbsent() = (inserted=%t, err=%v)", inserted, err)
	}
	if _, err := prepared.SubmitPreparedWithCallbacks(context.Background(), sessionID, turnprocessing.PreparedInput{Text: "private prompt"}, sessionruntime.TurnCallbacks{MessageID: "telegram-update:12"}); err != nil {
		t.Fatalf("SubmitPreparedWithCallbacks() error = %v", err)
	}
	if runtime.preparedCalls != 1 {
		t.Fatalf("prepared provider submissions = %d, want 1", runtime.preparedCalls)
	}
	if err := bundle.Finals.ProcessFinal(context.Background(), turnprocessing.FinalObservation{
		SessionID: sessionID, MessageID: "telegram-update:12", OperationID: "telegram-update:12:final", Text: "[final](file://" + artifactPath + ")",
	}); err != nil {
		t.Fatalf("P4 Finals.ProcessFinal() error = %v", err)
	}
	if documentCalls != 1 {
		t.Fatalf("final artifact documents = %d, want 1", documentCalls)
	}
	events, err := logger.Read(safelog.Service)
	if err != nil || len(events) != 1 || events[0].Fields["operation"] != "provider.codex.submit" {
		t.Fatalf("observability service events = %#v, %v", events, err)
	}
	serialized := events[0].EntityID + events[0].Fields["operation"] + events[0].Fields["total_ms"]
	for _, raw := range []string{string(sessionID), "telegram-update:12", "private prompt", runtime.final, artifactPath} {
		if strings.Contains(serialized, raw) {
			t.Fatalf("observability persisted raw input %q in %#v", raw, events[0])
		}
	}
}

type wiringRuntime struct {
	preparedCalls int
	final         string
}

func (runtime *wiringRuntime) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{Final: runtime.final, TerminalStatus: sessionruntime.StatusCompleted}, nil
}

func (runtime *wiringRuntime) SubmitWithCallbacks(ctx context.Context, id domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	runtime.preparedCalls++
	if callbacks.OnAccepted != nil {
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
	}
	return runtime.Submit(ctx, id, text)
}

func (runtime *wiringRuntime) SubmitStructuredWithCallbacks(ctx context.Context, id domain.SessionID, input sessionruntime.StructuredInput, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	return runtime.SubmitWithCallbacks(ctx, id, input.Text, callbacks)
}

func wiringConfiguration(t *testing.T, root string) (config.Config, string) {
	t.Helper()
	path := func(name string) string { return filepath.Join(root, name) }
	work := path("work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{
		"callback.key":       bytes.Repeat([]byte{'c'}, 32),
		"artifact-retry.key": bytes.Repeat([]byte{'a'}, 32),
		"parakeet.model":     []byte("model"),
		"parakeet":           []byte("binary"),
		"update.pub":         []byte("key"),
	} {
		mode := os.FileMode(0o600)
		if name == "parakeet" {
			mode = 0o700
		}
		wiringWritePrivate(t, path(name), contents, mode)
	}
	legacy := config.Config{
		OwnerUserID: 42, PrivateChatID: 42, BotUsername: "bria_test_bot", StatePath: path("state.json"),
		TelegramToken: config.TelegramTokenRef{EnvVar: "BRIA_TURNRUNTIME_WIRING_TOKEN"}, CallbackKey: config.CallbackKeyRef{SecretFile: path("callback.key")},
		Providers: map[string]config.ProviderConfig{"codex": {}, "claude": {}},
	}
	configuration, err := legacy.MigrateLegacy(config.LegacyMigration{
		Computer: config.ComputerConfig{ID: "wiring-computer", Name: "Wiring computer"}, Network: config.NetworkConfig{},
		Paths:  config.RuntimePaths{PairingPath: path("pairing.json"), CatalogPath: path("catalog.json"), FencePath: path("fence.json"), LedgerPath: path("ledger.json")},
		Update: config.UpdateConfig{SourceURL: "https://updates.example/manifest.json", TrustKeyFile: path("update.pub")}, Backup: config.BackupConfig{Destination: path("backup")},
		Parakeet:    config.ParakeetConfig{Executable: path("parakeet"), ModelPath: path("parakeet.model"), Argv: []string{"{model_path}"}},
		MediaLimits: config.MediaLimits{DownloadBytes: 1024, UploadBytes: 1024, VoiceBytes: 512, PhotoBytes: 512, TranscriptBytes: 512, DiagnosticBytes: 256},
		Runtime: &config.RuntimeFeatures{P4: &config.P4RuntimeConfig{
			VoiceTempDirectory: path("voice"), PhotoCustodyDirectory: path("photos"), ArtifactManifestDirectory: path("artifact-manifests"), ArtifactRetryDirectory: path("artifact-retries"),
			ArtifactAllowedRoots: []string{work}, ArtifactRetryKey: config.SecretFileRef{SecretFile: path("artifact-retry.key")}, ArtifactRetryTTLSeconds: 60,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return configuration, work
}

func wiringCanonicalTemp(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func wiringWritePrivate(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func wiringJSONResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
