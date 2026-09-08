package nativerecoverycomposition_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/nativeacceptance"
	"bria/internal/nativereceiptstore"
	"bria/internal/nativerecoverycomposition"
	"bria/internal/runtimefactory"
	"bria/internal/sessionruntime"
)

func TestConfiguredChildEnvironmentSelectsExactRecoveryTranscript(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "home", true: "codex-home"}[override], func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bria")
			for _, name := range []string{"bria", "bria-codex-adapter", "raw-codex"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("inert executable fixture"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.Config{OwnerUserID: 1, PrivateChatID: 1, BotUsername: "test_bot", TelegramToken: config.TelegramTokenRef{EnvVar: "TEST_TOKEN"}, CallbackKey: config.CallbackKeyRef{SecretFile: filepath.Join(root, "key")}, StatePath: filepath.Join(root, "state"), Providers: map[string]config.ProviderConfig{"codex": {Enabled: true, Command: &config.ProviderCommand{Exec: filepath.Join(root, "raw-codex"), Argv: []string{}}}, "claude": {Enabled: false}}}
			env := []string{"HOME=" + root, "PATH=/usr/bin"}
			nativeRoot := filepath.Join(root, ".codex", "sessions")
			if override {
				env = append(env, "CODEX_HOME="+filepath.Join(root, "custom"))
				nativeRoot = filepath.Join(root, "custom", "sessions")
			}
			commands, err := runtimefactory.NewCommandSet(cfg, env, bin)
			if err != nil {
				t.Fatal(err)
			}
			const nativeID = "00000000-0000-4000-8000-000000000077"
			if err = nativereceiptstore.Write(cfg.StatePath+".native", nativeacceptance.Document{SessionID: nativeID, Receipts: map[string]string{"message": "unknown"}, TurnIDs: map[string]string{"message": "turn"}}); err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(nativeRoot, 0700); err != nil {
				t.Fatal(err)
			}
			data := `{"type":"session_meta","payload":{"id":"` + nativeID + `","cwd":"/work"}}` + "\n" + `{"type":"turn_context","payload":{"turn_id":"turn"}}` + "\n" + `{"type":"event_msg","payload":{"type":"agent_message","phase":"final_answer","message":"Exact configured final"}}` + "\n" + `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn"}}` + "\n"
			if err = os.WriteFile(filepath.Join(nativeRoot, "rollout-"+nativeID+".jsonl"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			reader, err := nativerecoverycomposition.Compose(cfg, commands)
			if err != nil {
				t.Fatal(err)
			}
			result, err := reader.ReadAcceptedTurns(context.Background(), sessionruntime.AcceptedTurnReadRequest{SessionID: "logical", Provider: domain.ProviderCodex, Workdir: "/work", Binding: domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: nativeID, Generation: 1}})
			if err != nil || len(result.Turns) != 1 || result.Turns[0].Final != "Exact configured final" {
				t.Fatalf("configured read = %+v %v", result, err)
			}
		})
	}
}
