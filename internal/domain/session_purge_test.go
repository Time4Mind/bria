package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestPurgeArchivedSessionRemovesReplicatedArtifactsAndLeavesCarrierIdentity(t *testing.T) {
	state := fixtureState(t)
	if err := state.SetNodeAccess(3, domain.RoleMember, "alpha"); err != nil {
		t.Fatal(err)
	}
	ref := addSession(t, state, "purge", "alpha", 1, time.Unix(10, 0).UTC())
	if err := state.ShareSession(1, ref, 2, domain.ShareView); err != nil {
		t.Fatal(err)
	}
	session := state.Sessions[ref.Key()]
	if err := state.CloseSession(1, ref, session.Revision, "archive-purge", time.Unix(20, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	closed := state.Sessions[ref.Key()]
	if err := state.CompleteSessionArchive(1, ref, closed.Revision, closed.ArchiveID, time.Unix(21, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	ready := state.Sessions[ref.Key()]

	// These maps model presentation/runtime projections that can outlive an
	// archive card until the replicated purge reaches every viewer.
	for _, userID := range []domain.UserID{1, 2} {
		state.Navigation.ActiveNodeByUser[userID] = ref.NodeID
		state.Navigation.ActiveSessionByUserNode[userID] = map[domain.NodeID]domain.SessionID{
			ref.NodeID: ref.SessionID,
		}
		state.Navigation.SessionActivityByUser[userID] = map[string]time.Time{
			ref.Key(): time.Unix(22, 0).UTC(),
		}
		state.TelegramSessionViews[userID] = map[string]domain.TelegramSessionView{
			ref.Key(): {Page: 1, Pages: 1, Follow: true},
		}
		state.TelegramResponseCards[userID] = domain.TelegramResponseCard{
			ChatID: int64(userID), MessageID: int64(userID) + 10, Rich: true,
			RichMediaFileID: "media", PaneHash: "pane", ScreenHash: "screen",
			Session: ref, SessionRevision: ready.Revision,
			SessionEventAt: ready.LastEventAt, RenderedFinalAt: ready.LastEventAt,
		}
	}
	state.TelegramSessionViews[3] = map[string]domain.TelegramSessionView{
		"unrelated/keep": {Page: 1, Pages: 1, Follow: true},
	}
	state.TelegramResponseCards[3] = domain.TelegramResponseCard{
		ChatID: 3, MessageID: 13, Rich: true, RichMediaFileID: "keep-media",
	}
	state.Navigation.SessionActivityByUser[3] = map[string]time.Time{
		"unrelated/keep": time.Unix(23, 0).UTC(),
	}
	state.DeferredInputs[ref.Key()] = []domain.DeferredSessionInput{{
		Session: ref, OperationID: "queued", ExpectedGeneration: ready.RuntimeGeneration,
	}}
	state.Navigation.BackgroundByUser[1] = map[string]domain.BackgroundNotice{
		ref.Key(): {Session: ref},
	}

	purgedAt := time.Unix(30, 0).UTC()
	if err := state.PurgeArchivedSession(ref, ready.ArchiveID, ready.Revision, purgedAt); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Sessions[ref.Key()]; ok {
		t.Fatal("purged session record remained")
	}
	tombstone, ok := state.SessionTombstones[ref.Key()]
	if !ok || tombstone.Session != ref || tombstone.ArchiveID != ready.ArchiveID ||
		tombstone.RuntimeGeneration != ready.RuntimeGeneration ||
		!tombstone.PurgedAt.Equal(purgedAt) {
		t.Fatalf("tombstone=%#v, want ref/archive/time", tombstone)
	}
	if len(state.Grants) != 0 {
		t.Fatalf("session grants remained: %#v", state.Grants)
	}
	if _, ok := state.Navigation.SessionActivityByUser[1]; ok {
		t.Fatal("owner activity remained")
	}
	if _, ok := state.Navigation.SessionActivityByUser[2]; ok {
		t.Fatal("viewer activity remained")
	}
	if _, ok := state.Navigation.ActiveSessionByUserNode[1][ref.NodeID]; ok {
		t.Fatal("owner active session remained")
	}
	if _, ok := state.Navigation.ActiveSessionByUserNode[2][ref.NodeID]; ok {
		t.Fatal("viewer active session remained")
	}
	if _, ok := state.DeferredInputs[ref.Key()]; ok {
		t.Fatal("deferred input remained")
	}
	if _, ok := state.Navigation.BackgroundByUser[1][ref.Key()]; ok {
		t.Fatal("background notice remained")
	}
	for _, userID := range []domain.UserID{1, 2} {
		card := state.TelegramResponseCards[userID]
		if card.ChatID != int64(userID) || card.MessageID != int64(userID)+10 || card.Rich ||
			card.RichMediaFileID != "" || card.PaneHash != "" || card.ScreenHash != "" ||
			card.Session != (domain.SessionRef{}) || card.SessionRevision != 0 ||
			!card.SessionEventAt.IsZero() || !card.RenderedFinalAt.IsZero() {
			t.Fatalf("purged Telegram card for user %d = %#v", userID, card)
		}
		if _, ok := state.TelegramSessionViews[userID]; ok {
			t.Fatalf("purged Telegram view map for user %d remained", userID)
		}
	}
	if card := state.TelegramResponseCards[3]; card.MessageID != 13 ||
		card.RichMediaFileID != "keep-media" || !card.Rich {
		t.Fatalf("unrelated Telegram carrier changed: %#v", card)
	}
	if _, ok := state.TelegramSessionViews[3]["unrelated/keep"]; !ok {
		t.Fatal("unrelated Telegram view removed")
	}

	// Replaying the same purge after a crash is a no-op; changing the archive
	// identity is rejected instead of reusing the tombstoned session ref.
	if err := state.PurgeArchivedSession(ref, ready.ArchiveID, 1, purgedAt.Add(time.Hour)); err != nil {
		t.Fatalf("idempotent purge: %v", err)
	}
	if err := state.PurgeArchivedSession(ref, "different", 1, purgedAt); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("conflicting purge error=%v", err)
	}
	reused := domain.Session{
		ID: ref.SessionID, NodeID: ref.NodeID, OwnerID: 1, Backend: "codex",
		Workdir: "/srv/reused", State: domain.SessionLive, CreatedAt: purgedAt,
		LiveSinceAt: purgedAt, LastEventAt: purgedAt,
	}
	if err := state.AddSession(reused); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("stale AddSession error=%v", err)
	}
}

func TestSessionTombstonesPruneOldestByTimestampAndRefDeterministically(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "bounded", "alpha", 1, time.Unix(10, 0).UTC())
	session := state.Sessions[ref.Key()]
	if err := state.CloseSession(1, ref, session.Revision, "archive-bounded", time.Unix(20, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	closed := state.Sessions[ref.Key()]
	if err := state.CompleteSessionArchive(1, ref, closed.Revision, closed.ArchiveID, time.Unix(21, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < domain.MaxSessionTombstones-2; index++ {
		id := domain.SessionID("old-" + string(rune('a'+index%26)) + string(rune('0'+index/26)))
		oldest := time.Unix(100, 0).UTC().Add(time.Duration(index) * time.Second)
		state.SessionTombstones[(domain.SessionRef{NodeID: "alpha", SessionID: id}).Key()] = domain.SessionTombstone{
			Session: domain.SessionRef{NodeID: "alpha", SessionID: id}, ArchiveID: "archive-" + string(id),
			RuntimeGeneration: 1, PurgedAt: oldest,
		}
	}
	// Two equal timestamps exercise the lexical tie-break independently of
	// randomized Go map iteration.
	state.SessionTombstones[(domain.SessionRef{NodeID: "alpha", SessionID: "tie-b"}).Key()] = domain.SessionTombstone{
		Session: domain.SessionRef{NodeID: "alpha", SessionID: "tie-b"}, ArchiveID: "archive-tie-b",
		PurgedAt: time.Unix(1, 0).UTC(),
	}
	state.SessionTombstones[(domain.SessionRef{NodeID: "alpha", SessionID: "tie-a"}).Key()] = domain.SessionTombstone{
		Session: domain.SessionRef{NodeID: "alpha", SessionID: "tie-a"}, ArchiveID: "archive-tie-a",
		PurgedAt: time.Unix(1, 0).UTC(),
	}
	// The seed is exactly at the bound; purging adds one more and must retain
	// exactly the newest bound entries.
	if err := state.PurgeSession(ref, closed.ArchiveID, state.Sessions[ref.Key()].Revision, time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if got := len(state.SessionTombstones); got != domain.MaxSessionTombstones {
		t.Fatalf("tombstone count=%d, want %d", got, domain.MaxSessionTombstones)
	}
	if _, ok := state.SessionTombstones[(domain.SessionRef{NodeID: "alpha", SessionID: "tie-a"}).Key()]; ok {
		t.Fatal("lexicographically oldest tie tombstone survived")
	}
	if _, ok := state.SessionTombstones[(domain.SessionRef{NodeID: "alpha", SessionID: "tie-b"}).Key()]; !ok {
		t.Fatal("lexicographically newer tie tombstone was pruned")
	}
	if _, ok := state.SessionTombstones[ref.Key()]; !ok {
		t.Fatal("new purge tombstone was pruned")
	}
	clone := state.Clone()
	if len(clone.SessionTombstones) != domain.MaxSessionTombstones {
		t.Fatalf("cloned tombstone count=%d, want %d", len(clone.SessionTombstones), domain.MaxSessionTombstones)
	}
}

func TestPurgeArchivedSessionPreservesRestoreBeforeRetention(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "restore-first", "alpha", 1, time.Unix(10, 0).UTC())
	session := state.Sessions[ref.Key()]
	if err := state.CloseSession(1, ref, session.Revision, "archive-restore-first", time.Unix(20, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	closed := state.Sessions[ref.Key()]
	if err := state.PurgeArchivedSession(ref, closed.ArchiveID, closed.Revision, time.Unix(21, 0).UTC()); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("unready purge error=%v", err)
	}
	if err := state.CompleteSessionArchive(1, ref, closed.Revision, closed.ArchiveID, time.Unix(22, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	ready := state.Sessions[ref.Key()]
	if err := state.RestoreSession(1, ref, ready.Revision, time.Unix(23, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.SessionTombstones[ref.Key()]; ok {
		t.Fatal("restore created a purge tombstone")
	}
	if err := state.PurgeArchivedSession(ref, ready.ArchiveID, state.Sessions[ref.Key()].Revision, time.Unix(24, 0).UTC()); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("live purge error=%v", err)
	}
	if _, ok := state.Sessions[ref.Key()]; !ok {
		t.Fatal("restore-before-retention session disappeared")
	}
}

func TestPurgeArchivedSessionRejectsDuplicateArchiveIdentity(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "first", "alpha", 1, time.Unix(10, 0).UTC())
	first := state.Sessions[ref.Key()]
	if err := state.CloseSession(1, ref, first.Revision, "shared-archive", time.Unix(20, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	closed := state.Sessions[ref.Key()]
	if err := state.CompleteSessionArchive(1, ref, closed.Revision, closed.ArchiveID, time.Unix(21, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	duplicate := state.Sessions[ref.Key()]
	duplicate.ID = "second"
	duplicate.ArchiveID = closed.ArchiveID
	state.Sessions[duplicate.Ref().Key()] = duplicate

	err := state.PurgeArchivedSession(ref, closed.ArchiveID,
		state.Sessions[ref.Key()].Revision, time.Unix(30, 0).UTC())
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("duplicate archive purge error=%v", err)
	}
	if _, ok := state.Sessions[ref.Key()]; !ok {
		t.Fatal("duplicate archive purge mutated session state")
	}
}
