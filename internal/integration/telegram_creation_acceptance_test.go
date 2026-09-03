package integration_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionid"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	"bria/internal/telegramapp"
	"bria/internal/telegramui"
	workdirvalidator "bria/internal/workdir"
)

func TestNormalizedOwnerEventCreatesOneDurableReadySession(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve integration test executable: %v", err)
	}

	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		provider := provider
		t.Run(string(provider), func(t *testing.T) {
			const (
				ownerUserID     = int64(7000000001)
				ownerChatID     = int64(8000000002)
				updateID        = int64(9000000003)
				derivedIntentID = domain.IntentID("telegram-update:9000000003")
			)
			root := t.TempDir()
			workdir := filepath.Join(root, "confirmed-workdir")
			if err := os.Mkdir(workdir, 0o700); err != nil {
				t.Fatalf("create confirmed workdir: %v", err)
			}
			storePath := filepath.Join(root, "sessions.json")
			receiptPath := filepath.Join(root, "child-receipts.jsonl")
			intent := app.ConfirmedSessionIntent{
				IntentID:   domain.IntentID("caller-intent-" + string(provider)),
				ComputerID: "local-computer",
				Provider:   provider,
				Workdir:    workdir,
			}

			store, err := storage.OpenSessionStore(storePath)
			if err != nil {
				t.Fatalf("open session store: %v", err)
			}
			starter, err := sessionruntime.NewStarter(
				map[domain.Provider]sessionruntime.CommandSpec{
					provider: {
						Path: testBinary,
						Args: []string{"-test.run=^TestAcceptanceProviderChild$"},
						Env: []string{
							helperProcessEnvironment + "=1",
							"BRIA_ACCEPTANCE_STORE=" + storePath,
							"BRIA_ACCEPTANCE_INTENT=" + string(derivedIntentID),
							"BRIA_ACCEPTANCE_RECEIPT=" + receiptPath,
						},
					},
				},
				sessionruntime.Options{HandshakeTimeout: 2 * time.Second},
			)
			if err != nil {
				t.Fatalf("create real process starter: %v", err)
			}
			ids, err := sessionid.NewWithReader(bytes.NewReader(bytes.Repeat([]byte{0x11}, 64)))
			if err != nil {
				t.Fatalf("create deterministic session id source: %v", err)
			}
			creator, err := app.NewSessionCreator(
				intent.ComputerID,
				workdirvalidator.ExistingDirectory{},
				ids,
				store,
				starter,
			)
			if err != nil {
				t.Fatalf("create application session creator: %v", err)
			}
			handler, err := telegramapp.NewHandler(ownerUserID, ownerChatID, creator)
			if err != nil {
				t.Fatalf("create normalized Telegram handler: %v", err)
			}

			authorized := telegramapp.ConfirmedSessionCreation{
				UpdateID:     updateID,
				SenderUserID: ownerUserID,
				ChatID:       ownerChatID,
				ChatKind:     telegramapp.ChatPrivate,
				Intent:       intent,
			}
			unauthorized := []struct {
				name  string
				event telegramapp.ConfirmedSessionCreation
			}{
				{
					name: "wrong sender",
					event: func() telegramapp.ConfirmedSessionCreation {
						copy := authorized
						copy.SenderUserID++
						return copy
					}(),
				},
				{
					name: "different private chat",
					event: func() telegramapp.ConfirmedSessionCreation {
						copy := authorized
						copy.ChatID++
						return copy
					}(),
				},
			}
			for _, attempt := range unauthorized {
				receipt, err := handler.HandleConfirmedSessionCreation(context.Background(), attempt.event)
				if err != nil {
					t.Fatalf("%s returned error: %v", attempt.name, err)
				}
				want := telegramapp.CreationReceipt{
					Disposition: telegramapp.DispositionIgnoredUnauthorized,
				}
				if !reflect.DeepEqual(receipt, want) {
					t.Fatalf("%s receipt = %#v, want non-disclosing %#v", attempt.name, receipt, want)
				}
				if _, ok, err := store.GetByIntent(context.Background(), derivedIntentID); err != nil || ok {
					t.Fatalf("%s durable session = (%v, %v), want absent", attempt.name, ok, err)
				}
				if got := countReceipts(t, receiptPath, "start"); got != 0 {
					t.Fatalf("%s physical child starts = %d, want 0", attempt.name, got)
				}
				if _, err := os.Stat(storePath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s store stat error = %v, want not-exist", attempt.name, err)
				}
			}

			nonDirectory := filepath.Join(root, "not-a-directory")
			if err := os.WriteFile(nonDirectory, []byte("file"), 0o600); err != nil {
				t.Fatalf("create non-directory workdir fixture: %v", err)
			}
			invalidWorkdirs := []struct {
				name string
				path string
			}{
				{name: "missing", path: filepath.Join(root, "missing-workdir")},
				{name: "regular file", path: nonDirectory},
			}
			for _, invalid := range invalidWorkdirs {
				invalidEvent := authorized
				invalidEvent.Intent.Workdir = invalid.path
				receipt, err := handler.HandleConfirmedSessionCreation(context.Background(), invalidEvent)
				if err == nil {
					t.Fatalf("%s workdir error = nil, want rejection", invalid.name)
				}
				if !reflect.DeepEqual(receipt, telegramapp.CreationReceipt{}) {
					t.Fatalf("%s workdir receipt = %#v, want zero receipt", invalid.name, receipt)
				}
				if _, ok, err := store.GetByIntent(context.Background(), derivedIntentID); err != nil || ok {
					t.Fatalf("%s workdir durable session = (%v, %v), want absent", invalid.name, ok, err)
				}
				if got := countReceipts(t, receiptPath, "start"); got != 0 {
					t.Fatalf("%s workdir physical child starts = %d, want 0", invalid.name, got)
				}
				if _, err := os.Stat(storePath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s workdir store stat error = %v, want not-exist", invalid.name, err)
				}
			}

			created, err := handler.HandleConfirmedSessionCreation(context.Background(), authorized)
			if err != nil {
				t.Fatalf("authorized event returned error: %v", err)
			}
			if got, want := created.Disposition, telegramapp.DispositionCreatedReady; got != want {
				t.Fatalf("created disposition = %q, want %q", got, want)
			}
			if got, want := created.UpdateID, updateID; got != want {
				t.Errorf("created update id = %d, want %d", got, want)
			}
			if got, want := created.IntentID, derivedIntentID; got != want {
				t.Errorf("created intent id = %q, want %q", got, want)
			}
			if got, want := created.SessionID, domain.SessionID("11111111-1111-4111-9111-111111111111"); got != want {
				t.Errorf("created session id = %q, want deterministic %q", got, want)
			}
			if got, want := created.Status, domain.SessionReady; got != want {
				t.Errorf("created status = %q, want %q", got, want)
			}
			if created.ProviderBinding == nil {
				t.Fatal("created receipt has no provider binding")
			}
			if created.Card == nil {
				t.Fatal("created receipt has no card")
			}
			wantCard := telegramui.SessionCard{
				Computer: intent.ComputerID,
				Provider: intent.Provider,
				Workdir:  intent.Workdir,
				State:    telegramui.SessionReady,
			}
			if got := *created.Card; got != wantCard {
				t.Errorf("created card = %#v, want %#v", got, wantCard)
			}
			startReceipt := waitForReceipt(t, receiptPath, "start", 2*time.Second)
			if got, want := startReceipt.ProviderSessionID, created.ProviderBinding.SessionID; got != want {
				t.Errorf("physical provider session = %q, receipt binding %q", got, want)
			}
			aliveReceipt := waitForReceipt(t, receiptPath, "alive", 2*time.Second)
			if got, want := aliveReceipt.ProviderSessionID, created.ProviderBinding.SessionID; got != want {
				t.Errorf("live provider session = %q, receipt binding %q", got, want)
			}

			request := app.StartSessionRequest{
				SessionID:  created.SessionID,
				ComputerID: intent.ComputerID,
				Provider:   intent.Provider,
				Workdir:    intent.Workdir,
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err := starter.Abort(ctx, request, *created.ProviderBinding); err != nil {
					t.Errorf("abort acceptance helper: %v", err)
				}
			})

			reopened, err := storage.OpenSessionStore(storePath)
			if err != nil {
				t.Fatalf("reopen store from disk: %v", err)
			}
			persisted, ok, err := reopened.GetByIntent(context.Background(), derivedIntentID)
			if err != nil {
				t.Fatalf("read session from reopened store: %v", err)
			}
			if !ok {
				t.Fatal("reopened store has no authorized session")
			}
			if got, want := persisted.ID(), created.SessionID; got != want {
				t.Errorf("persisted session id = %q, card receipt session id %q", got, want)
			}
			persistedBinding, ok := persisted.Binding()
			if !ok || persistedBinding != *created.ProviderBinding {
				t.Errorf("persisted binding = (%#v, %v), want %#v", persistedBinding, ok, *created.ProviderBinding)
			}
			if got := telegramui.ProjectSessionCard(persisted); got != *created.Card {
				t.Errorf("persisted card identity = %#v, emitted card %#v", got, *created.Card)
			}
			if _, ok, err := reopened.GetByIntent(context.Background(), intent.IntentID); err != nil || ok {
				t.Fatalf("caller-supplied intent id was persisted = (%v, %v), want absent", ok, err)
			}

			samePayloadReplay := authorized
			samePayloadReplay.Intent.IntentID = domain.IntentID("different-caller-replay-" + string(provider))
			replayed, err := handler.HandleConfirmedSessionCreation(context.Background(), samePayloadReplay)
			if err != nil {
				t.Fatalf("replay same normalized event: %v", err)
			}
			if got, want := replayed.Disposition, telegramapp.DispositionReplayed; got != want {
				t.Fatalf("replay disposition = %q, want %q", got, want)
			}
			if replayed.UpdateID != created.UpdateID ||
				replayed.IntentID != created.IntentID ||
				replayed.SessionID != created.SessionID ||
				replayed.Status != created.Status ||
				replayed.ProviderBinding == nil ||
				*replayed.ProviderBinding != *created.ProviderBinding ||
				replayed.Card == nil ||
				*replayed.Card != *created.Card {
				t.Fatalf("replay receipt = %#v, want same identity/card as %#v", replayed, created)
			}
			time.Sleep(100 * time.Millisecond)
			if got := countReceipts(t, receiptPath, "start"); got != 1 {
				t.Fatalf("physical child starts after replay = %d, want 1", got)
			}

			conflictingReplay := authorized
			conflictingReplay.Intent.IntentID = domain.IntentID("different-caller-conflict-" + string(provider))
			conflictingReplay.Intent.Provider = otherProvider(provider)
			conflictingReplay.Intent.Workdir = filepath.Join(root, "different-confirmed-workdir")
			conflictReceipt, err := handler.HandleConfirmedSessionCreation(
				context.Background(),
				conflictingReplay,
			)
			if !errors.Is(err, app.ErrIntentConflict) {
				t.Fatalf("conflicting replay error = %v, want %v", err, app.ErrIntentConflict)
			}
			if !reflect.DeepEqual(conflictReceipt, telegramapp.CreationReceipt{}) {
				t.Fatalf("conflicting replay receipt = %#v, want zero receipt", conflictReceipt)
			}
			persistedAfterConflict, ok, err := reopened.GetByIntent(context.Background(), derivedIntentID)
			if err != nil || !ok {
				t.Fatalf("read original after conflict = (%v, %v)", ok, err)
			}
			if !persistedAfterConflict.Equal(persisted) {
				t.Fatalf(
					"conflict mutated durable session to %#v, want original %#v",
					persistedAfterConflict.Snapshot(),
					persisted.Snapshot(),
				)
			}
			if got := countReceipts(t, receiptPath, "start"); got != 1 {
				t.Fatalf("physical child starts after conflict = %d, want 1", got)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := starter.Abort(ctx, request, *created.ProviderBinding); err != nil {
				t.Fatalf("abort acceptance helper through protocol: %v", err)
			}
			closeReceipt := waitForReceipt(t, receiptPath, "close", 2*time.Second)
			if got, want := closeReceipt.ProviderSessionID, created.ProviderBinding.SessionID; got != want {
				t.Errorf("closed provider session = %q, binding %q", got, want)
			}
		})
	}
}
