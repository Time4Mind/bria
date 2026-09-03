package settingscomposition_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"bria/internal/app"
	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/telegramcontroller"
)

func TestPreferencesMutationsPersistAndLocalReloadSharesOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	preferences := settingscomposition.Preferences{Store: store}
	mutations := []struct {
		name  string
		apply func() error
		check func(t *testing.T, got settings.Settings)
	}{
		{"continue", func() error { return preferences.ToggleContinueExisting(context.Background()) }, func(t *testing.T, got settings.Settings) {
			if got.ContinueExisting {
				t.Fatal("continue_existing remained enabled")
			}
		}},
		{"screen", func() error { return preferences.ToggleScreen(context.Background()) }, func(t *testing.T, got settings.Settings) {
			if !got.ScreenEnabled {
				t.Fatal("screen remained disabled")
			}
		}},
		{"detail", func() error { return preferences.ToggleCardDetail(context.Background()) }, func(t *testing.T, got settings.Settings) {
			if got.CardDetail != settings.CardDetailCompact {
				t.Fatalf("detail=%q", got.CardDetail)
			}
		}},
		{"technical", func() error { return preferences.ToggleTechnicalActions(context.Background()) }, func(t *testing.T, got settings.Settings) {
			if got.ShowTechnicalActions {
				t.Fatal("technical actions remained enabled")
			}
		}},
		{"questions", func() error { return preferences.ToggleBackgroundQuestions(context.Background()) }, func(t *testing.T, got settings.Settings) {
			if got.NotifyBackgroundQuestions {
				t.Fatal("questions remained enabled")
			}
		}},
		{"errors", func() error { return preferences.ToggleBackgroundErrors(context.Background()) }, func(t *testing.T, got settings.Settings) {
			if got.NotifyBackgroundErrors {
				t.Fatal("errors remained enabled")
			}
		}},
		{"lifetime", func() error { return preferences.SetSessionLifetime(context.Background(), "48h") }, func(t *testing.T, got settings.Settings) {
			if got.SessionLifetime != settings.Lifetime48Hours {
				t.Fatalf("lifetime=%q", got.SessionLifetime)
			}
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if err := mutation.apply(); err != nil {
				t.Fatal(err)
			}
			reopened, err := settings.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reopened.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			mutation.check(t, got)
		})
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := preferences.SetSessionLifetime(context.Background(), "3h"); err == nil {
		t.Fatal("invalid lifetime was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("invalid lifetime wrote settings: equal=%t, err=%v", bytes.Equal(after, before), err)
	}

	localEdit := bytes.Replace(before, []byte(`"queue_limit": 32`), []byte(`"queue_limit": 41`), 1)
	if bytes.Equal(localEdit, before) {
		t.Fatal("settings fixture has no queue limit")
	}
	if err := os.WriteFile(path, localEdit, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := preferences.Snapshot(context.Background())
	if err != nil || snapshot.QueueLimit != 41 {
		t.Fatalf("Telegram snapshot after local reload = %#v, %v", snapshot, err)
	}
}

func TestPreferencesDriveTypedControllerAndDurableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := telegramcontroller.New(42, 42, "local", settingsControllerCreator{}, settingsControllerSessions{}, settingsControllerSubmit{}, settingsControllerNotifier{}, telegramcontroller.Options{Settings: settingscomposition.Preferences{Store: store}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuSettings})
	if err != nil || result.Surface == nil || !strings.Contains(result.Surface.Text, "Срок жизни сессий: never") {
		t.Fatalf("settings surface = (%#v, %v)", result, err)
	}
	for _, action := range []telegramcontroller.SemanticActionKind{
		telegramcontroller.SemanticSettingsContinueExisting, telegramcontroller.SemanticSettingsScreen,
		telegramcontroller.SemanticSettingsDetail, telegramcontroller.SemanticSettingsTechnicalActions,
		telegramcontroller.SemanticSettingsBackgroundQuestions, telegramcontroller.SemanticSettingsBackgroundErrors,
		telegramcontroller.SemanticSettingsLifetime48Hours,
	} {
		if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: action}); err != nil {
			t.Fatalf("typed action %q: %v", action, err)
		}
	}
	reopened, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Load(context.Background())
	if err != nil || persisted.ContinueExisting || !persisted.ScreenEnabled || persisted.CardDetail != settings.CardDetailCompact || persisted.ShowTechnicalActions || persisted.NotifyBackgroundQuestions || persisted.NotifyBackgroundErrors || persisted.SessionLifetime != settings.Lifetime48Hours {
		t.Fatalf("durable controller settings = %+v, %v", persisted, err)
	}
}

type settingsControllerCreator struct{}

func (settingsControllerCreator) Create(context.Context, app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
	return app.CreateSessionResult{}, nil
}

type settingsControllerSessions struct{}

func (settingsControllerSessions) List(context.Context) ([]domain.Session, error) { return nil, nil }
func (settingsControllerSessions) Load(context.Context, domain.SessionID) (domain.Session, error) {
	return domain.Session{}, errors.New("not found")
}

type settingsControllerSubmit struct{}

func (settingsControllerSubmit) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, nil
}

type settingsControllerNotifier struct{}

func (settingsControllerNotifier) Notify(context.Context, telegramcontroller.Notification) error {
	return nil
}

func TestProviderPreferencesPersistWithoutMutatingCommandsOrSecretReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bria.json")
	if err := os.WriteFile(path, []byte(providerConfigFixture(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	preferences := settingscomposition.ProviderPreferences{Store: store}
	if err := preferences.ToggleProvider(context.Background(), domain.ProviderCodex); err != nil {
		t.Fatal(err)
	}
	disabled, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.ProviderEnabled(domain.ProviderCodex) || !reflect.DeepEqual(disabled.Providers["codex"].Command, initial.Config.Providers["codex"].Command) ||
		disabled.TelegramToken != initial.Config.TelegramToken || disabled.CallbackKey != initial.Config.CallbackKey {
		t.Fatalf("disabled config lost non-toggle state: %#v", disabled)
	}
	if _, err := store.SetProviderEnabled(context.Background(), initial.Revision, domain.ProviderCodex, true); !errors.Is(err, config.ErrRevisionConflict) {
		t.Fatalf("stale provider CAS error = %v, want conflict", err)
	}
	if err := preferences.ToggleProvider(context.Background(), domain.ProviderCodex); err != nil {
		t.Fatal(err)
	}
	reenabled, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reenabled.ProviderEnabled(domain.ProviderCodex) || !reflect.DeepEqual(reenabled.Providers["codex"].Command, initial.Config.Providers["codex"].Command) ||
		reenabled.TelegramToken != initial.Config.TelegramToken || reenabled.CallbackKey != initial.Config.CallbackKey {
		t.Fatalf("reenabled config lost non-toggle state: %#v", reenabled)
	}
}

func providerConfigFixture(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	return `{
  "owner_user_id": 42,
  "private_chat_id": 42,
  "bot_username": "bria_test_bot",
  "state_path": ` + strconv.Quote(filepath.Join(directory, "state.json")) + `,
  "telegram_token": {"env_var": "BRIA_TELEGRAM_TOKEN"},
  "callback_key": {"secret_file": ` + strconv.Quote(filepath.Join(directory, "callback.key")) + `},
  "providers": {
    "codex": {"enabled": true, "command": {"exec": ` + strconv.Quote(executable) + `, "argv": ["serve"]}},
    "claude": {"enabled": false}
  }
}`
}
