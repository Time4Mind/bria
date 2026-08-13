package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestManualArchiveBecomesRestorableOnlyAfterArtifactCommit(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "restore-me", "alpha", 1, time.Unix(10, 0).UTC())
	before := state.Sessions[ref.Key()]
	closedAt := time.Unix(20, 0).UTC()
	if err := state.CloseSession(1, ref, before.Revision, "archive-restore", closedAt); err != nil {
		t.Fatal(err)
	}
	closed := state.Sessions[ref.Key()]
	if closed.ArchiveReady {
		t.Fatal("archive became restorable before its artifact was committed")
	}
	if err := state.RestoreSession(1, ref, closed.Revision, time.Unix(21, 0).UTC()); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("early restore error=%v", err)
	}
	if err := state.CompleteSessionArchive(
		1, ref, closed.Revision, closed.ArchiveID, time.Unix(22, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	ready := state.Sessions[ref.Key()]
	restoredAt := time.Unix(30, 0).UTC()
	if err := state.RestoreSession(1, ref, ready.Revision, restoredAt); err != nil {
		t.Fatal(err)
	}
	restored := state.Sessions[ref.Key()]
	if !restored.IsLive() || restored.RuntimePhase != domain.RuntimeDegraded ||
		!restored.ResumePending || restored.RuntimeGeneration != before.RuntimeGeneration+1 {
		t.Fatalf("restored session=%#v", restored)
	}
	if restored.LiveSinceAt != restoredAt || restored.ArchiveID != "archive-restore" {
		t.Fatalf("restored timestamps or provenance=%#v", restored)
	}
}

func TestRestoreRequiresOwnerAndOnlineOrigin(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "restore-acl", "alpha", 1, time.Unix(10, 0).UTC())
	if err := state.ShareSession(1, ref, 2, domain.ShareControl); err != nil {
		t.Fatal(err)
	}
	closed := state.Sessions[ref.Key()]
	if err := state.CloseSession(1, ref, closed.Revision, "archive-acl", time.Unix(20, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	closed = state.Sessions[ref.Key()]
	if err := state.CompleteSessionArchive(
		1, ref, closed.Revision, closed.ArchiveID, time.Unix(21, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	ready := state.Sessions[ref.Key()]
	if err := state.RestoreSession(2, ref, ready.Revision, time.Unix(22, 0).UTC()); !errors.Is(err, domain.ErrAccessDenied) {
		t.Fatalf("shared controller restore error=%v", err)
	}
	node := state.Nodes[ref.NodeID]
	node.Status = domain.NodeOffline
	state.Nodes[ref.NodeID] = node
	if err := state.RestoreSession(1, ref, ready.Revision, time.Unix(23, 0).UTC()); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("offline restore error=%v", err)
	}
}

func TestGeneratedRenameNeverOverwritesANewerName(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "rename", "alpha", 1, time.Unix(10, 0).UTC())
	session := state.Sessions[ref.Key()]
	if err := state.RenameSession(
		1, ref, session.Revision, "fresh context", time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if err := state.RenameSession(
		1, ref, session.Revision, "stale result", time.Unix(21, 0).UTC(),
	); !errors.Is(err, domain.ErrStaleOperation) {
		t.Fatalf("stale rename error=%v", err)
	}
	if got := state.Sessions[ref.Key()].Name; got != "fresh context" {
		t.Fatalf("name=%q", got)
	}
}

func TestHostRebootPreservesCommittedStartingIntent(t *testing.T) {
	state := fixtureState(t)
	created := time.Unix(100, 0).UTC()
	session := domain.Session{
		ID: "starting", NodeID: "alpha", OwnerID: 1, Backend: "codex",
		Workdir: "/srv/project", State: domain.SessionLive,
		RuntimePhase: domain.RuntimeStarting, CreatedAt: created,
		LiveSinceAt: created, LastEventAt: created,
	}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	plan, err := state.ObserveNodeBoot("alpha", "new-boot", created.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Archived) != 0 || len(plan.Recover) != 0 {
		t.Fatalf("recovery plan=%#v", plan)
	}
	got := state.Sessions[session.Ref().Key()]
	if !got.IsLive() || got.RuntimePhase != domain.RuntimeStarting {
		t.Fatalf("starting intent=%#v", got)
	}
}
