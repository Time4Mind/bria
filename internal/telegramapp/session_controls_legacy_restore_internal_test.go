package telegramapp

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/sessionstart"
)

type legacyRestoreStarterStub struct {
	candidates []sessionstart.ProviderCandidate
}

func (s legacyRestoreStarterStub) Browse(
	context.Context, application.Principal, domain.NodeID, string,
) (sessionstart.BrowseResult, error) {
	return sessionstart.BrowseResult{}, nil
}

func (s legacyRestoreStarterStub) Discover(
	_ context.Context,
	_ application.Principal,
	_ domain.NodeID,
	_, _ string,
	offset, limit int,
) (sessionstart.ProviderPage, error) {
	end := min(len(s.candidates), offset+limit)
	if offset >= end {
		return sessionstart.ProviderPage{Total: len(s.candidates)}, nil
	}
	return sessionstart.ProviderPage{
		Items: append([]sessionstart.ProviderCandidate(nil), s.candidates[offset:end]...),
		Total: len(s.candidates),
	}, nil
}

func (s legacyRestoreStarterStub) Create(
	context.Context, application.Principal, application.CreateSessionRequest,
) (domain.Session, error) {
	return domain.Session{}, nil
}

func TestLegacyArchiveRestoreRequiresUniqueCreationMatch(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	session := domain.Session{
		ID: "legacy", NodeID: "node", Backend: "codex", Workdir: "/work",
		CreatedAt: created,
	}
	handler := &Handler{starter: legacyRestoreStarterStub{candidates: []sessionstart.ProviderCandidate{
		{ID: "unrelated", CreatedAt: created.Add(3 * time.Second)},
		{ID: "matched", CreatedAt: created.Add(time.Second)},
	}}}
	providerID, err := handler.discoverMissingProvider(
		context.Background(), application.Principal{UserID: 1}, session,
	)
	if err != nil || providerID != "matched" {
		t.Fatalf("provider=%q err=%v", providerID, err)
	}
	handler.starter = legacyRestoreStarterStub{candidates: []sessionstart.ProviderCandidate{
		{ID: "matched-a", CreatedAt: created.Add(time.Second)},
		{ID: "matched-b", CreatedAt: created.Add(-time.Second)},
	}}
	if _, err := handler.discoverMissingProvider(
		context.Background(), application.Principal{UserID: 1}, session,
	); err == nil {
		t.Fatal("ambiguous provider discovery was accepted")
	}
}
