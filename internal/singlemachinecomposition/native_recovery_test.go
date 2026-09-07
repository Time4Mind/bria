package singlemachinecomposition

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/recoveryruntime"
	"bria/internal/sessionruntime"
)

func TestNativeRecoveryCompositionDoesNotRequireDiscoveryOrStartCLI(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	configuration := config.Config{OwnerUserID: 1, PrivateChatID: 1, BotUsername: "test_bot", TelegramToken: config.TelegramTokenRef{EnvVar: "BRIA_TEST_TOKEN"}, CallbackKey: config.CallbackKeyRef{SecretFile: filepath.Join(root, "key")}, StatePath: filepath.Join(root, "state.json"), Providers: map[string]config.ProviderConfig{"codex": {Enabled: true, Command: &config.ProviderCommand{Exec: executable, Argv: []string{}}}, "claude": {Enabled: true, Command: &config.ProviderCommand{Exec: executable, Argv: []string{}}}}}
	if err := configuration.Validate(); err != nil {
		t.Fatal(err)
	}
	// Nil CommandSet intentionally proves reconciliation no longer requires an
	// adapter executable or legacy Claude SDK transcript markers.
	reader, err := composeAcceptedTurnReader(configuration, nil)
	if err != nil || reader == nil {
		t.Fatalf("compose=%T %v", reader, err)
	}
	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		_, err := reader.ReadAcceptedTurns(context.Background(), sessionruntime.AcceptedTurnReadRequest{SessionID: "logical", Provider: provider, Workdir: "/work", Binding: domain.ProviderBinding{Provider: provider, SessionID: "00000000-0000-4000-8000-000000000077", Generation: 1}})
		if !errors.Is(err, recoveryruntime.ErrUnavailable) {
			t.Fatalf("missing %s=%v", provider, err)
		}
	}
}
