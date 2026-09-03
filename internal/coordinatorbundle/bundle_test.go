package coordinatorbundle_test

import (
	"testing"
	"time"

	"bria/internal/computer"
	"bria/internal/coordinator"
	"bria/internal/coordinatorbundle"
	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/settings"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestBundleValidatesCompleteCredentialFreeCoordinatorState(t *testing.T) {
	bundle := validBundle()
	digest, err := bundle.Digest()
	if err != nil || len(digest) != 64 {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	if bundle.CallbackRegistry.Presentations["session-1"].Tokens["token-a"] || bundle.CallbackVerificationKeyID == "" {
		t.Fatal("callback replay manifest or verification key identity missing")
	}
}

func TestBundleRejectsLossyOrUnboundState(t *testing.T) {
	for name, mutate := range map[string]func(*coordinatorbundle.Bundle){
		"missing session": func(bundle *coordinatorbundle.Bundle) { bundle.Sessions = nil },
		"journal gap":     func(bundle *coordinatorbundle.Bundle) { bundle.Inputs = nil },
		"carrier mismatch": func(bundle *coordinatorbundle.Bundle) {
			value := bundle.CallbackRegistry.Presentations["session-1"]
			value.Carrier.MessageID++
			bundle.CallbackRegistry.Presentations["session-1"] = value
		},
		"foreign chat": func(bundle *coordinatorbundle.Bundle) {
			bundle.TelegramScope.PrivateChatID = 99
		},
		"missing callback operations": func(bundle *coordinatorbundle.Bundle) { bundle.CallbackOperations.Operations = nil },
		"missing key identity":        func(bundle *coordinatorbundle.Bundle) { bundle.CallbackVerificationKeyID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			bundle := validBundle()
			mutate(&bundle)
			if err := bundle.Validate(); err == nil {
				t.Fatal("invalid bundle accepted")
			}
		})
	}
}

func TestBundleAcceptsExactlyBoundGlobalMenuRecovery(t *testing.T) {
	bundle := validBundle()
	carrier := telegramstate.Carrier{ChatID: bundle.TelegramScope.PrivateChatID, MessageID: 20}
	bundle.CallbackRegistry.Presentations[telegramui.GlobalSurfaceID] = telegrampipeline.CallbackPresentationSnapshot{SessionID: telegramui.GlobalSurfaceID, Carrier: carrier, ExpiresAt: time.Date(2026, 9, 3, 11, 0, 0, 0, time.UTC), Tokens: map[string]bool{"global-token": true}, Claims: map[string]telegrampipeline.CallbackClaimSnapshot{"global-token": {UpdateID: 20, CallbackQueryID: "global-query"}}}
	bundle.CallbackOperations.Operations["global-operation"] = telegramflow.CallbackOperation{ID: "global-operation", UpdateID: 20, CallbackQueryID: "global-query", CallbackDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Plan: telegrampipeline.CallbackPlan{OperationID: "global-operation", UpdateID: 20, SessionID: telegramui.GlobalSurfaceID, Carrier: carrier, Action: telegramui.ActionMenuSettings, Effect: telegrampipeline.EffectOpenSettings}, Phase: telegramflow.CallbackEffectUnknown}
	if err := bundle.Validate(); err != nil {
		t.Fatalf("global recovery rejected: %v", err)
	}
	forged := bundle
	operation := forged.CallbackOperations.Operations["global-operation"]
	operation.Plan.Carrier.ChatID++
	forged.CallbackOperations.Operations["global-operation"] = operation
	if err := forged.Validate(); err == nil {
		t.Fatal("foreign-chat global operation accepted")
	}
}

func validBundle() coordinatorbundle.Bundle {
	created := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	carrier := telegramstate.Carrier{ChatID: 1, MessageID: 10}
	return coordinatorbundle.Bundle{
		Version:       coordinatorbundle.Version,
		Catalog:       computer.CatalogSnapshot{Computers: []computer.Record{{ID: "new", Name: "New", Fingerprint: "sha256:new", Status: computer.StatusOnline, ProtocolVersion: 1}}},
		Routes:        []coordinatorbundle.Route{{TelegramMessageID: 10, SessionID: "session-1", ComputerID: "new"}},
		Settings:      settings.Snapshot{Revision: 1, Settings: settings.Default()},
		Sessions:      []domain.SessionSnapshot{{ID: "session-1", IntentID: "intent-1", ComputerID: "new", Provider: domain.ProviderCodex, Workdir: "/workspace", Status: domain.SessionStarting, CreatedAt: created, StateChangedAt: created}},
		TelegramScope: coordinatorbundle.TelegramScope{BotID: 100, OwnerUserID: 7, PrivateChatID: 1},
		TelegramUI: telegramstate.State{Version: telegramstate.FormatVersion, ActiveSession: "session-1", Cards: map[domain.SessionID]telegramstate.Card{
			"session-1": {SessionID: "session-1", Carrier: carrier, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}},
		}},
		Journals:                  []coordinatorbundle.JournalSession{{SessionID: "session-1", NextSequence: 1}},
		Inputs:                    []messagejournal.Input{{MessageID: "input-1", SessionID: "session-1", Sequence: 1, Phase: messagejournal.InputCompleted}},
		Checkpoint:                coordinator.StoredCheckpoint{Revision: 1, Checkpoint: coordinator.Checkpoint{Initialized: true, NextUpdateID: 11}},
		CallbackVerificationKeyID: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		CallbackRegistry: telegrampipeline.CallbackRegistrySnapshot{Version: telegrampipeline.CallbackRegistrySnapshotVersion, Presentations: map[domain.SessionID]telegrampipeline.CallbackPresentationSnapshot{
			"session-1": {SessionID: "session-1", Carrier: carrier, ExpiresAt: created.Add(time.Hour), Tokens: map[string]bool{"token-a": false}, Claims: map[string]telegrampipeline.CallbackClaimSnapshot{}},
		}},
		CallbackOperations: telegramflow.CallbackStateSnapshot{Version: telegramflow.CallbackStateSnapshotVersion, Operations: map[string]telegramflow.CallbackOperation{}, Statuses: map[string]telegramflow.StatusOperation{}},
	}
}
