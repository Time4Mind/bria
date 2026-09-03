package telegrampipeline

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/telegramstate"
)

func TestFileCallbackRegistryRequiresDirectorySyncAndReopensWholePostRenameSnapshot(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	path := filepath.Join(t.TempDir(), "callback-registry.json")
	registry, err := OpenFileCallbackRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	directorySyncFailure := errors.New("directory sync failure")
	syncCalls := 0
	registry.syncDirectory = func(directory string) error {
		syncCalls++
		if directory != filepath.Dir(path) {
			t.Fatalf("synced directory = %q, want %q", directory, filepath.Dir(path))
		}
		return directorySyncFailure
	}
	presentation := CallbackPresentation{
		SessionID: "123e4567-e89b-12d3-a456-426614174000",
		Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 99},
		TokenIDs:  []string{"token"},
		ExpiresAt: now.Add(time.Minute),
	}
	if err := registry.Replace(context.Background(), presentation); !errors.Is(err, directorySyncFailure) {
		t.Fatalf("Replace() error = %v, want directory sync failure", err)
	}
	if syncCalls != 1 {
		t.Fatalf("directory sync calls = %d, want 1", syncCalls)
	}

	// Rename has happened but durability acknowledgement failed. A restart may
	// observe either old or new state after a real power loss; when the new file
	// is present, it must reopen as one whole validated snapshot.
	reopened, err := OpenFileCallbackRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("reopen post-rename snapshot: %v", err)
	}
	result, err := reopened.Claim(context.Background(), CallbackClaim{
		SessionID:       domain.SessionID(presentation.SessionID),
		Carrier:         presentation.Carrier,
		TokenID:         "token",
		ExpiresAt:       presentation.ExpiresAt,
		UpdateID:        1,
		CallbackQueryID: "query",
	})
	if err != nil || result.Outcome != ClaimAccepted {
		t.Fatalf("reopened whole snapshot claim = %#v, %v", result, err)
	}
}

func TestFileCallbackRegistryRecoversOnlyExactClaimAfterRestart(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	path := filepath.Join(t.TempDir(), "callback-registry.json")
	registry, err := OpenFileCallbackRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presentation := CallbackPresentation{
		SessionID: "123e4567-e89b-12d3-a456-426614174000",
		Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 99},
		TokenIDs:  []string{"token"},
		ExpiresAt: now.Add(time.Minute),
	}
	if err := registry.Replace(context.Background(), presentation); err != nil {
		t.Fatal(err)
	}
	claim := CallbackClaim{
		SessionID: presentation.SessionID, Carrier: presentation.Carrier,
		TokenID: "token", ExpiresAt: presentation.ExpiresAt,
		UpdateID: 101, CallbackQueryID: "query-101",
	}
	if result, err := registry.Claim(context.Background(), claim); err != nil || result.Outcome != ClaimAccepted {
		t.Fatalf("initial claim = %#v, %v", result, err)
	}
	reopened, err := OpenFileCallbackRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if result, err := reopened.Claim(context.Background(), claim); err != nil || result.Outcome != ClaimRecovered {
		t.Fatalf("exact recovered claim = %#v, %v", result, err)
	}
	different := claim
	different.UpdateID++
	different.CallbackQueryID = "query-102"
	if result, err := reopened.Claim(context.Background(), different); err != nil || result.Outcome != ClaimReplayed {
		t.Fatalf("different replay claim = %#v, %v", result, err)
	}
}
