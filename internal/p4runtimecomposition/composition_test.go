package p4runtimecomposition_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/p4runtimecomposition"
	"bria/internal/providerinputcomposition"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/speech"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/turnprocessing"
)

func TestOpenWithoutRuntimeP4IsDisabledWithoutDependenciesOrFilesystemWrites(t *testing.T) {
	bundle, enabled, err := p4runtimecomposition.Open(p4runtimecomposition.Options{Configuration: config.Config{}})
	if err != nil || enabled || bundle != nil {
		t.Fatalf("Open() = (%#v, %t, %v), want disabled nil bundle", bundle, enabled, err)
	}
}

func TestOpenExplicitP4RuntimeRoutesOpaquePhotoCustodyToCodexRuntime(t *testing.T) {
	const (
		token       = "987654:p4-photo-test"
		photoID     = "telegram-photo-id"
		photoUnique = "telegram-photo-unique"
	)
	photo := []byte("a complete bounded photo payload")
	httpCalls := 0
	telegramClient := mustP4TelegramClient(t, token, p4HTTPClientFunc(func(request *http.Request) (*http.Response, error) {
		httpCalls++
		switch request.URL.Path {
		case "/bot" + token + "/getFile":
			if request.Method != http.MethodPost {
				return nil, fmt.Errorf("getFile method = %s", request.Method)
			}
			var getFile telegram.GetFileRequest
			if err := json.NewDecoder(request.Body).Decode(&getFile); err != nil {
				return nil, err
			}
			if getFile.FileID != photoID {
				return nil, fmt.Errorf("getFile file id = %q", getFile.FileID)
			}
			return p4JSONResponse(fmt.Sprintf(`{"ok":true,"result":{"file_id":"telegram-photo-id","file_unique_id":"telegram-photo-unique","file_size":%d,"file_path":"photos/p4-photo.jpg"}}`, len(photo))), nil
		case "/file/bot" + token + "/photos/p4-photo.jpg":
			if request.Method != http.MethodGet {
				return nil, fmt.Errorf("photo download method = %s", request.Method)
			}
			response := p4BytesResponse(photo)
			response.Header.Set("Content-Length", fmt.Sprintf("%d", len(photo)))
			return response, nil
		default:
			return nil, fmt.Errorf("unexpected Telegram endpoint %s", request.URL.Path)
		}
	}))

	root := canonicalP4TempDir(t)
	configuration := migratedP4Configuration(t, root, writeP4RegularFile(t, root, "parakeet.model", []byte("model"), 0o600))
	preferences, err := settings.OpenFileStore(filepath.Join(root, "settings.json"))
	if err != nil {
		t.Fatalf("OpenFileStore() error = %v", err)
	}
	sessions, err := storage.OpenSessionStore(filepath.Join(root, "sessions.json"))
	if err != nil {
		t.Fatalf("OpenSessionStore() error = %v", err)
	}
	runtime := &p4Runtime{}
	bundle, enabled, err := p4runtimecomposition.Open(p4runtimecomposition.Options{
		Configuration: configuration, Telegram: telegramClient, Settings: preferences, Sessions: sessions, Runtime: runtime,
	})
	if err != nil || !enabled || bundle == nil {
		t.Fatalf("Open() = (%#v, %t, %v), want enabled bundle", bundle, enabled, err)
	}
	p4, ok := configuration.P4Runtime()
	if !ok {
		t.Fatal("P4Runtime() = disabled after explicit migration")
	}
	for _, directory := range []string{p4.VoiceTempDirectory, p4.PhotoCustodyDirectory} {
		info, err := os.Stat(directory)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("P4 physical directory %q = (%#v, %v), want private directory", directory, info, err)
		}
	}

	const sessionID domain.SessionID = "11111111-1111-4111-8111-111111111111"
	session, err := domain.NewStartingSessionAt(sessionID, "p4-photo-intent", "p4-test-computer", domain.ProviderCodex, root, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC), domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := sessions.PutStartingIfAbsent(context.Background(), session); err != nil || !inserted {
		t.Fatalf("PutStartingIfAbsent() = (inserted=%t, err=%v)", inserted, err)
	}
	persisted, err := sessions.Load(context.Background(), sessionID)
	if err != nil || !persisted.Equal(session) || persisted.Provider() != domain.ProviderCodex {
		t.Fatalf("Load() = (%#v, %v), want persisted Codex session", persisted.Snapshot(), err)
	}

	structuredPreparer, ok := bundle.InputPreparer.(turnprocessing.StructuredInputPreparer)
	if !ok {
		t.Fatalf("InputPreparer %T does not expose structured public seam", bundle.InputPreparer)
	}
	prepared, err := structuredPreparer.PrepareStructured(context.Background(), turnprocessing.IncomingInput{
		Kind: "photo", FileID: photoID, FileUniqueID: photoUnique, FileSize: int64(len(photo)),
		MIMEType: "image/jpeg", Width: 640, Height: 480, DownloadPermitted: true,
	})
	if err != nil {
		t.Fatalf("PrepareStructured(photo) error = %v", err)
	}
	if prepared.Text != "" || len(prepared.Attachments) != 1 {
		t.Fatalf("prepared photo = %#v, want one structured opaque attachment", prepared)
	}
	attachment := prepared.Attachments[0]
	digest := sha256.Sum256(photo)
	if attachment.Size != int64(len(photo)) || attachment.SHA256 != hex.EncodeToString(digest[:]) ||
		strings.ContainsAny(attachment.Reference, `/\\`) {
		t.Fatalf("prepared attachment = %#v, want opaque exact digest", attachment)
	}
	custodyPath := filepath.Join(p4.PhotoCustodyDirectory, attachment.Reference, "photo")
	if !filepath.IsAbs(custodyPath) || strings.Contains(prepared.Text, custodyPath) || attachment.Reference == custodyPath {
		t.Fatalf("prepared input leaked raw custody path: %#v", prepared)
	}
	if stored, err := os.ReadFile(custodyPath); err != nil || !bytes.Equal(stored, photo) {
		t.Fatalf("photo custody before provider resolution = (%q, %v), want exact durable bytes", stored, err)
	}
	if len(runtime.structuredInputs) != 0 {
		t.Fatalf("runtime received input before custody resolver: %#v", runtime.structuredInputs)
	}

	preparedSubmitter, ok := bundle.Submitter.(turnprocessing.PreparedTurnSubmitter)
	if !ok {
		t.Fatalf("Submitter %T does not expose structured public seam", bundle.Submitter)
	}
	_, err = preparedSubmitter.SubmitPreparedWithCallbacks(context.Background(), sessionID, prepared, sessionruntime.TurnCallbacks{MessageID: "telegram:photo:87"})
	if err != nil {
		t.Fatalf("SubmitPreparedWithCallbacks() error = %v", err)
	}
	if httpCalls != 2 || len(runtime.structuredInputs) != 1 {
		t.Fatalf("Telegram HTTP calls=%d structured runtime calls=%d, want 2 and 1", httpCalls, len(runtime.structuredInputs))
	}
	runtimeInput := runtime.structuredInputs[0]
	if runtimeInput.Text != "" || len(runtimeInput.Attachments) != 1 {
		t.Fatalf("runtime structured input = %#v", runtimeInput)
	}
	local := runtimeInput.Attachments[0]
	if local.Path != custodyPath || !filepath.IsAbs(local.Path) || local.Size != int64(len(photo)) || local.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("runtime local attachment = %#v, want absolute verified custody file", local)
	}
	if delivered, err := os.ReadFile(local.Path); err != nil || !bytes.Equal(delivered, photo) {
		t.Fatalf("runtime custody file = (%q, %v), want exact photo bytes", delivered, err)
	}

	if err := bundle.Attachments.MarkAccepted(context.Background(), turnprocessing.AttachmentReceipt{
		Reference: attachment.Reference, ProviderSession: "codex-provider-session", MessageID: "telegram:photo:87",
	}); err != nil {
		t.Fatalf("MarkAccepted() error = %v", err)
	}
	if err := bundle.Attachments.MarkCompleted(context.Background(), turnprocessing.AttachmentReceipt{
		Reference: attachment.Reference, ProviderSession: "codex-provider-session", MessageID: "telegram:photo:87",
	}); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}
	if _, err := os.Stat(custodyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed custody payload stat error = %v, want released payload", err)
	}
}

func TestOpenP4RuntimeScreenGateUsesFileSettingsAndConfirmedTelegramReceipt(t *testing.T) {
	const token = "987655:p4-screen-test"
	sendPhotoCalls := 0
	telegramClient := mustP4TelegramClient(t, token, p4HTTPClientFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/bot"+token+"/sendPhoto" {
			return nil, fmt.Errorf("unexpected Telegram request %s %s", request.Method, request.URL.Path)
		}
		sendPhotoCalls++
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			return nil, err
		}
		if request.FormValue("chat_id") != "42" {
			return nil, fmt.Errorf("screen chat id = %q", request.FormValue("chat_id"))
		}
		photo, header, err := request.FormFile("photo")
		if err != nil {
			return nil, err
		}
		defer photo.Close()
		content, err := io.ReadAll(photo)
		if err != nil || header.Filename == "" || !bytes.HasPrefix(content, []byte("\x89PNG\r\n\x1a\n")) {
			return nil, fmt.Errorf("screen PNG = (name=%q bytes=%d err=%v)", header.Filename, len(content), err)
		}
		return p4JSONResponse(`{"ok":true,"result":{"message_id":912,"from":{"id":600,"is_bot":true,"first_name":"Bria"},"chat":{"id":42,"type":"private"},"photo":[{"file_id":"screen-photo","file_unique_id":"screen-photo-unique","width":1,"height":1,"file_size":3}]}}`), nil
	}))

	root := canonicalP4TempDir(t)
	configuration := migratedP4Configuration(t, root, writeP4RegularFile(t, root, "parakeet.model", []byte("model"), 0o600))
	settingsPath := filepath.Join(root, "settings.json")
	preferences, err := settings.OpenFileStore(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := storage.OpenSessionStore(filepath.Join(root, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	bundle, enabled, err := p4runtimecomposition.Open(p4runtimecomposition.Options{
		Configuration: configuration, Telegram: telegramClient, Settings: preferences, Sessions: sessions, Runtime: &p4Runtime{},
	})
	if err != nil || !enabled || bundle == nil || bundle.RuntimeEvents == nil {
		t.Fatalf("Open() = (%#v, %t, %v), want enabled RuntimeEvents", bundle, enabled, err)
	}

	const sessionID domain.SessionID = "22222222-2222-4222-8222-222222222222"
	disabled := turnprocessing.RuntimeEventObservation{
		OperationID: "p4-screen:1", SessionID: sessionID, MessageID: "telegram:screen:1", EventIndex: 1,
		Event: sessionruntime.TurnEvent{Kind: sessionruntime.EventCommentary, Text: "first safe line"},
	}
	if err := bundle.RuntimeEvents.ObserveRuntimeEvent(context.Background(), disabled); err != nil {
		t.Fatalf("ObserveRuntimeEvent(disabled) error = %v", err)
	}
	if sendPhotoCalls != 0 {
		t.Fatalf("disabled Screen sent %d photos", sendPhotoCalls)
	}
	if err := preferences.Update(context.Background(), func(value *settings.Settings) error {
		value.ScreenEnabled = true
		return nil
	}); err != nil {
		t.Fatalf("FileStore.Update() error = %v", err)
	}
	if info, err := os.Stat(settingsPath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("persisted settings file = (%#v, %v), want private regular file", info, err)
	}
	reopened, err := settings.OpenFileStore(settingsPath)
	if err != nil {
		t.Fatalf("reopen settings file: %v", err)
	}
	if value, err := reopened.Load(context.Background()); err != nil || !value.ScreenEnabled {
		t.Fatalf("reloaded Screen setting = (%#v, %v), want enabled", value, err)
	}

	enabledObservation := disabled
	enabledObservation.OperationID = "p4-screen:2"
	enabledObservation.EventIndex = 2
	enabledObservation.Event.Text = "second safe line"
	if err := bundle.RuntimeEvents.ObserveRuntimeEvent(context.Background(), enabledObservation); err != nil {
		t.Fatalf("ObserveRuntimeEvent(enabled) error = %v", err)
	}
	if sendPhotoCalls != 1 {
		t.Fatalf("enabled Screen sendPhoto calls = %d, want exact confirmed receipt delivery", sendPhotoCalls)
	}
}

func TestOpenP4RuntimeFailsClosedAtFirstVoiceForMissingOrSymlinkedModel(t *testing.T) {
	for _, test := range []struct {
		name  string
		model func(*testing.T, string) string
	}{
		{
			name: "missing model",
			model: func(_ *testing.T, root string) string {
				return filepath.Join(root, "missing-private-model.bin")
			},
		},
		{
			name: "symlinked model",
			model: func(t *testing.T, root string) string {
				target := writeP4RegularFile(t, root, "real-private-model.bin", []byte("model"), 0o600)
				link := filepath.Join(root, "private-model.link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			const token = "987656:p4-voice-test"
			voice := []byte("voice")
			httpCalls := 0
			telegramClient := mustP4TelegramClient(t, token, p4HTTPClientFunc(func(request *http.Request) (*http.Response, error) {
				httpCalls++
				switch request.URL.Path {
				case "/bot" + token + "/getFile":
					return p4JSONResponse(`{"ok":true,"result":{"file_id":"voice-file","file_unique_id":"voice-unique","file_size":5,"file_path":"voice/p4.oga"}}`), nil
				case "/file/bot" + token + "/voice/p4.oga":
					response := p4BytesResponse(voice)
					response.Header.Set("Content-Length", "5")
					return response, nil
				default:
					return nil, fmt.Errorf("unexpected Telegram endpoint %s", request.URL.Path)
				}
			}))
			root := canonicalP4TempDir(t)
			configuration := migratedP4Configuration(t, root, test.model(t, root))
			preferences, err := settings.OpenFileStore(filepath.Join(root, "settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			sessions, err := storage.OpenSessionStore(filepath.Join(root, "sessions.json"))
			if err != nil {
				t.Fatal(err)
			}
			bundle, enabled, err := p4runtimecomposition.Open(p4runtimecomposition.Options{
				Configuration: configuration, Telegram: telegramClient, Settings: preferences, Sessions: sessions, Runtime: &p4Runtime{},
			})
			if err != nil || !enabled || bundle == nil {
				t.Fatalf("Open() = (%#v, %t, %v), want enabled composition before voice boundary", bundle, enabled, err)
			}
			prepared, err := bundle.InputPreparer.Prepare(context.Background(), turnprocessing.IncomingInput{
				Kind: "voice", FileID: "voice-file", FileUniqueID: "voice-unique", FileSize: int64(len(voice)),
				MIMEType: "audio/ogg", DurationSeconds: 1, DownloadPermitted: true,
			})
			if prepared != "" || !errors.Is(err, speech.ErrInvalidConfiguration) {
				t.Fatalf("Prepare(voice) = (%q, %v), want fail-closed invalid model", prepared, err)
			}
			if httpCalls != 2 {
				t.Fatalf("voice preparation HTTP calls = %d, want bounded getFile/download only", httpCalls)
			}
		})
	}
}

type p4Runtime struct {
	structuredInputs []sessionruntime.StructuredInput
}

var _ providerinputcomposition.Runtime = (*p4Runtime)(nil)

func (runtime *p4Runtime) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{Final: "ok", TerminalStatus: sessionruntime.StatusCompleted}, nil
}

func (runtime *p4Runtime) SubmitWithCallbacks(_ context.Context, _ domain.SessionID, _ string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	if callbacks.OnAccepted != nil {
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
	}
	return sessionruntime.TurnResult{Final: "ok", TerminalStatus: sessionruntime.StatusCompleted}, nil
}

func (runtime *p4Runtime) SubmitStructuredWithCallbacks(_ context.Context, _ domain.SessionID, input sessionruntime.StructuredInput, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	runtime.structuredInputs = append(runtime.structuredInputs, input)
	if callbacks.OnAccepted != nil {
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
	}
	return sessionruntime.TurnResult{Final: "ok", TerminalStatus: sessionruntime.StatusCompleted}, nil
}

type p4HTTPClientFunc func(*http.Request) (*http.Response, error)

func (function p4HTTPClientFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func mustP4TelegramClient(t *testing.T, token string, client telegram.HTTPClient) *telegram.Client {
	t.Helper()
	result, err := telegram.NewClient(token, client, telegram.Options{MaxDownloadBytes: 1 << 20, MaxUploadBytes: 1 << 20})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return result
}

func p4JSONResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func p4BytesResponse(body []byte) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
}

func canonicalP4TempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	return root
}

func writeP4RegularFile(t *testing.T, root, name string, content []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatalf("write %q: %v", name, err)
	}
	return path
}

func migratedP4Configuration(t *testing.T, root, modelPath string) config.Config {
	t.Helper()
	path := func(name string) string { return filepath.Join(root, name) }
	for _, file := range []string{"callback.key", "artifact-retry.key", "update.pub", "parakeet"} {
		mode := os.FileMode(0o600)
		if file == "parakeet" {
			mode = 0o700
		}
		writeP4RegularFile(t, root, file, []byte(file), mode)
	}
	legacy := config.Config{
		OwnerUserID: 42, PrivateChatID: 42, BotUsername: "bria_test_bot", StatePath: path("state.json"),
		TelegramToken: config.TelegramTokenRef{EnvVar: "BRIA_P4_TEST_TOKEN"}, CallbackKey: config.CallbackKeyRef{SecretFile: path("callback.key")},
		Providers: map[string]config.ProviderConfig{"codex": {}, "claude": {}},
	}
	configuration, err := legacy.MigrateLegacy(config.LegacyMigration{
		Computer: config.ComputerConfig{ID: "p4-test-computer", Name: "P4 test computer"},
		Network:  config.NetworkConfig{},
		Paths: config.RuntimePaths{
			PairingPath: path("pairing.json"), CatalogPath: path("catalog.json"), FencePath: path("fence.json"), LedgerPath: path("ledger.json"),
		},
		Update: config.UpdateConfig{SourceURL: "https://updates.example/manifest.json", TrustKeyFile: path("update.pub")},
		Backup: config.BackupConfig{Destination: path("backup")},
		Parakeet: config.ParakeetConfig{
			Executable: path("parakeet"), ModelPath: modelPath, Argv: []string{"{model_path}"},
		},
		MediaLimits: config.MediaLimits{
			DownloadBytes: 1024, UploadBytes: 1024, VoiceBytes: 512, PhotoBytes: 512, TranscriptBytes: 512, DiagnosticBytes: 256,
		},
		Runtime: &config.RuntimeFeatures{P4: &config.P4RuntimeConfig{
			VoiceTempDirectory: path("voice-temp"), PhotoCustodyDirectory: path("photo-custody"),
			ArtifactManifestDirectory: path("artifact-manifests"), ArtifactRetryDirectory: path("artifact-retries"),
			ArtifactAllowedRoots: []string{path("artifact-root")}, ArtifactRetryKey: config.SecretFileRef{SecretFile: path("artifact-retry.key")}, ArtifactRetryTTLSeconds: 60,
		}},
	})
	if err != nil {
		t.Fatalf("MigrateLegacy() error = %v", err)
	}
	return configuration
}
