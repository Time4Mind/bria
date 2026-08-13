package archive_test

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/archive"
	"github.com/Time4Mind/bria/internal/domain"
)

func manifest(id, node, session string, owner domain.UserID, archivedAt time.Time) archive.Manifest {
	content := []byte(id)
	return archive.Manifest{
		Version:    archive.ManifestVersion,
		ID:         archive.ArchiveID(id),
		Session:    domain.SessionRef{NodeID: domain.NodeID(node), SessionID: domain.SessionID(session)},
		OwnerID:    owner,
		Backend:    "codex",
		CreatedAt:  archivedAt.Add(-time.Hour),
		ArchivedAt: archivedAt,
		Reason:     domain.ArchiveIdle,
		Artifact: archive.ArtifactMetadata{
			Format:    "bria-session-v1",
			MediaType: "application/x-bria-session+tar",
			SizeBytes: int64(len(content)),
			Integrity: archive.SHA256Digest(content),
		},
	}
}

func TestPlanPurgeIsPureDueInclusiveAndDeterministicallySorted(t *testing.T) {
	base := time.Unix(1000, 0).UTC()
	now := base.Add(30 * 24 * time.Hour)
	candidates := []archive.PurgeCandidate{
		{
			Manifest: manifest("later", "node-b", "session-b", 2, now.Add(-13*24*time.Hour)),
			Policy:   archive.RetentionPolicy{Days: archive.Retention14Days, Action: archive.ExpiryFull},
		},
		{
			Manifest: manifest("second", "node-b", "session-a", 1, base),
			Policy:   archive.RetentionPolicy{Days: archive.Retention30Days, Action: archive.ExpiryFull},
		},
		{
			Manifest: manifest("first", "node-a", "session-z", 1, base),
			Policy:   archive.RetentionPolicy{Days: archive.Retention14Days, Action: archive.ExpiryRecordOnly},
		},
		{
			Manifest: manifest("forever", "node-a", "session-a", 1, base),
			Policy:   archive.RetentionPolicy{Days: archive.RetentionUnlimited, Action: archive.ExpiryFull},
		},
	}

	plan, err := archive.PlanPurge(now, candidates)
	if err != nil {
		t.Fatalf("PlanPurge: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("plan length = %d, want 2: %#v", len(plan), plan)
	}
	if plan[0].ArchiveID != "first" || plan[0].Action != archive.ExpiryRecordOnly {
		t.Fatalf("first decision = %#v", plan[0])
	}
	if plan[1].ArchiveID != "second" || plan[1].Action != archive.ExpiryFull {
		t.Fatalf("second decision = %#v", plan[1])
	}
	if candidates[0].Manifest.ID != "later" {
		t.Fatal("planner mutated candidates")
	}
}

func TestPlanPurgeRejectsDuplicateArchiveIdentity(t *testing.T) {
	archivedAt := time.Unix(100, 0).UTC()
	candidate := archive.PurgeCandidate{
		Manifest: manifest("same", "node-a", "one", 1, archivedAt),
		Policy:   archive.RetentionPolicy{Days: archive.Retention14Days, Action: archive.ExpiryFull},
	}
	other := candidate
	other.Manifest.Session.SessionID = "two"

	if _, err := archive.PlanPurge(archivedAt.Add(30*24*time.Hour), []archive.PurgeCandidate{candidate, other}); err == nil {
		t.Fatal("duplicate archive ID unexpectedly accepted")
	}
}
