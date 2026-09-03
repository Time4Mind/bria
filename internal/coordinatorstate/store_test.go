package coordinatorstate_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"bria/internal/computer"
	"bria/internal/coordinator"
	"bria/internal/coordinatorstate"
	"bria/internal/coordinatortransfer"
	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/settings"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestStateStoreStagesAppliesAndRereadsTypedSnapshot(t *testing.T) {
	store, err := coordinatorstate.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := validSnapshot()
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("fixture bundle: %v", err)
	}
	receipt, err := store.Stage(context.Background(), "transfer-1", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(context.Background(), receipt.TransferID, receipt.Digest); err != nil {
		t.Fatal(err)
	}
	reopened, err := coordinatorstate.Open(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	got, gotReceipt, err := reopened.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gotDigest, _ := got.Digest()
	if gotReceipt != receipt || gotDigest != receipt.Digest {
		t.Fatalf("reread receipt=%#v digest=%s", gotReceipt, gotDigest)
	}
	if got.CallbackOperations.Operations["callback-operation-1"].Phase != telegramflow.CallbackEffectUnknown {
		t.Fatalf("callback recovery state was not reread: %#v", got.CallbackOperations)
	}
	if len(got.Sessions) != 1 || got.TelegramUI.ActiveSession != "session-1" || len(got.CallbackRegistry.Presentations) != 1 {
		t.Fatalf("session/UI/callback registry state was not reread: %#v", got)
	}
}

func TestStateStoreRejectsSemanticInvalidAndSecretBearingSnapshot(t *testing.T) {
	store, _ := coordinatorstate.Open(t.TempDir())
	invalid := validSnapshot()
	invalid.Routes[0].ComputerID = "missing"
	if _, err := store.Stage(context.Background(), "transfer-1", invalid); err == nil {
		t.Fatal("invalid route accepted")
	}
	for index, material := range []string{
		"-----BEGIN PRIVATE KEY-----",
		"ANTHROPIC_API_KEY=sk-sensitive",
		"Authorization: Bearer bearer-sensitive",
		`{"api_key":"sensitive"}`,
		`{"password":"x"}`,
		`{"secret_file":"/run/secrets/callback"}`,
	} {
		secret := validSnapshot()
		secret.Inputs[0].Payload = []byte(material)
		if _, err := store.Stage(context.Background(), "secret-transfer", secret); !errors.Is(err, coordinatorstate.ErrSensitiveState) {
			t.Fatalf("secret case %d error=%v", index, err)
		}
	}
	invalidCallback := validSnapshot()
	invalidCallback.Checkpoint.Checkpoint.Outbound.Phase = ""
	if _, err := store.Stage(context.Background(), "transfer-3", invalidCallback); !errors.Is(err, coordinatorstate.ErrInvalidState) {
		t.Fatalf("invalid callback operation error=%v", err)
	}
}

func TestStateStoreRollbackRestoresPreviousRereadableVersion(t *testing.T) {
	store, _ := coordinatorstate.Open(t.TempDir())
	first := validSnapshot()
	firstReceipt, _ := store.Stage(context.Background(), "transfer-1", first)
	_ = store.Apply(context.Background(), firstReceipt.TransferID, firstReceipt.Digest)
	second := validSnapshot()
	second.Settings.Revision = 2
	secondReceipt, _ := store.Stage(context.Background(), "transfer-2", second)
	_ = store.Apply(context.Background(), secondReceipt.TransferID, secondReceipt.Digest)
	if err := store.Rollback(context.Background(), "transfer-2"); err != nil {
		t.Fatal(err)
	}
	reopened, err := coordinatorstate.Open(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, err := reopened.Read(context.Background())
	if err != nil || receipt != firstReceipt {
		t.Fatalf("rollback receipt=%#v err=%v", receipt, err)
	}
}

func TestStateStoreRequiresExplicitRecoveryAndRestoresPreActivationVersion(t *testing.T) {
	root := t.TempDir()
	store, _ := coordinatorstate.Open(root)
	firstReceipt, _ := store.Stage(context.Background(), "transfer-1", validSnapshot())
	if err := store.Apply(context.Background(), firstReceipt.TransferID, firstReceipt.Digest); err != nil {
		t.Fatal(err)
	}
	second := validSnapshot()
	second.Settings.Revision = 2
	secondReceipt, _ := store.Stage(context.Background(), "transfer-2", second)

	previous := map[string]any{"version": 1, "transfer_id": firstReceipt.TransferID, "digest": firstReceipt.Digest}
	next := map[string]any{"version": 1, "transfer_id": secondReceipt.TransferID, "digest": secondReceipt.Digest}
	writeJSON(t, filepath.Join(root, "activation.json"), map[string]any{"version": 1, "previous": previous, "next": next})
	writeJSON(t, filepath.Join(root, "active.json"), next)

	if _, _, err := store.Read(context.Background()); !errors.Is(err, coordinatorstate.ErrRecoveryRequired) {
		t.Fatalf("live handle read during interrupted activation error=%v", err)
	}
	if _, err := coordinatorstate.Open(root); !errors.Is(err, coordinatorstate.ErrRecoveryRequired) {
		t.Fatalf("open during interrupted activation error=%v", err)
	}
	if err := coordinatorstate.RecoverInterruptedRollback(root); err != nil {
		t.Fatal(err)
	}
	reopened, err := coordinatorstate.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, err := reopened.Read(context.Background())
	if err != nil || receipt != firstReceipt {
		t.Fatalf("recovered receipt=%#v err=%v", receipt, err)
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func validSnapshot() coordinatortransfer.Snapshot {
	created := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	session := domain.SessionSnapshot{ID: "session-1", IntentID: "intent-1", ComputerID: "new", Provider: domain.ProviderCodex, Workdir: "/workspace", Status: domain.SessionStarting, CreatedAt: created, StateChangedAt: created}
	carrier := telegramstate.Carrier{ChatID: 1, MessageID: 10}
	return coordinatortransfer.Snapshot{
		Version: coordinatortransfer.SnapshotVersion,
		Catalog: computer.CatalogSnapshot{Computers: []computer.Record{{
			ID: "new", Name: "New", Fingerprint: "sha256:new",
			Status: computer.StatusOnline, ProtocolVersion: 1,
		}}},
		Routes:        []coordinatortransfer.Route{{TelegramMessageID: 10, SessionID: "session-1", ComputerID: "new"}},
		Settings:      settings.Snapshot{Revision: 1, Settings: settings.Default()},
		Sessions:      []domain.SessionSnapshot{session},
		TelegramScope: coordinatortransfer.TelegramScope{BotID: 100, OwnerUserID: 7, PrivateChatID: 1},
		TelegramUI: telegramstate.State{Version: telegramstate.FormatVersion, ActiveSession: "session-1", Cards: map[domain.SessionID]telegramstate.Card{
			"session-1": {SessionID: "session-1", Carrier: carrier, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}},
		}},
		Journals: []coordinatortransfer.JournalSession{{SessionID: "session-1", NextSequence: 2}},
		Inputs: []messagejournal.Input{{
			MessageID: "input-1", SessionID: "session-1", Sequence: 1,
			Payload: []byte("hello"), Phase: messagejournal.InputPending,
		}},
		Outputs: []messagejournal.Output{{
			OperationID: "output-1", SessionID: "session-1", Sequence: 2,
			Kind: "final", Phase: messagejournal.OutputPending,
		}},
		Checkpoint: coordinator.StoredCheckpoint{Revision: 1, Checkpoint: coordinator.Checkpoint{
			Initialized:  true,
			NextUpdateID: 10,
			Outbound: &coordinator.Outbound{
				OperationID: "callback-operation-1",
				UpdateID:    10,
				Status: coordinator.Status{
					ConversationID: 1, Text: "working", CallbackQueryID: "callback-query-1", SourceMessageID: 10,
				},
				Phase: coordinator.OutboundPrepared,
			},
		}},
		CallbackVerificationKeyID: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		CallbackRegistry: telegrampipeline.CallbackRegistrySnapshot{Version: telegrampipeline.CallbackRegistrySnapshotVersion, Presentations: map[domain.SessionID]telegrampipeline.CallbackPresentationSnapshot{
			"session-1": {SessionID: "session-1", Carrier: carrier, ExpiresAt: created.Add(time.Hour), Tokens: map[string]bool{"token-a": false}, Claims: map[string]telegrampipeline.CallbackClaimSnapshot{}},
		}},
		CallbackOperations: telegramflow.CallbackStateSnapshot{
			Version: telegramflow.CallbackStateSnapshotVersion,
			Operations: map[string]telegramflow.CallbackOperation{
				"callback-operation-1": {
					ID: "callback-operation-1", UpdateID: 10,
					CallbackQueryID: "callback-query-1",
					CallbackDigest:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
					Plan: telegrampipeline.CallbackPlan{
						OperationID: "callback-operation-1", UpdateID: 10,
						SessionID: "session-1", Carrier: carrier,
						Action: telegramui.ActionOptions, Effect: telegrampipeline.EffectToggleOptions,
					},
					Phase: telegramflow.CallbackEffectUnknown,
				},
			},
			Statuses: map[string]telegramflow.StatusOperation{},
		},
	}
}

func TestStateStorePersistsCallbackOperationStateButNoSecretContainer(t *testing.T) {
	root := t.TempDir()
	store, err := coordinatorstate.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Stage(context.Background(), "transfer-1", validSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "versions", receipt.Digest, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(manifest)
	if !strings.Contains(text, `"callback_operation_state_included":true`) ||
		!strings.Contains(text, `"callback_signing_secrets_included":false`) {
		t.Fatalf("callback state policy is not explicit: %s", text)
	}
	entries, err := os.ReadDir(filepath.Join(root, "versions", receipt.Digest))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if strings.Contains(name, "secret") || strings.Contains(name, "credential") || strings.Contains(name, "token") {
			t.Fatalf("secret container persisted: %s", entry.Name())
		}
	}
}

func TestOpenSecuresOwnedDirectoriesAndRejectsSymlinkRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows directory privacy is enforced by ACL")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinatorstate.Open(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Join(root, "versions")} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("private directory %s mode=%v", path, info.Mode().Perm())
		}
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinatorstate.Open(alias); !errors.Is(err, coordinatorstate.ErrInvalidState) {
		t.Fatalf("symlink root error=%v", err)
	}
}
