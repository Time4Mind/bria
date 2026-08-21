package domain_test

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestReattachSessionRuntimeRepairsOnlyUnmaterializedResumeFailure(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "survivor", "alpha", 1, time.Unix(10, 0).UTC())
	session := state.Sessions[ref.Key()]
	if err := state.ArchiveSession(
		ref, session.Revision, domain.ArchiveResumeFailed, time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	archived := state.Sessions[ref.Key()]
	restoredAt := time.Unix(30, 0).UTC()
	if err := state.ReattachSessionRuntime(
		ref, archived.RuntimeGeneration, archived.Revision, restoredAt,
	); err != nil {
		t.Fatal(err)
	}
	got := state.Sessions[ref.Key()]
	if !got.IsLive() || got.RuntimePhase != domain.RuntimeIdle ||
		got.ArchiveReason != "" || !got.ArchivedAt.IsZero() || got.RestoredAt != restoredAt {
		t.Fatalf("reattached session=%#v", got)
	}
}

func TestReattachSessionRuntimeRejectsMaterializedArchive(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "materialized", "alpha", 1, time.Unix(10, 0).UTC())
	session := state.Sessions[ref.Key()]
	if err := state.MarkMissingOnSameBoot(
		ref, "missing-runtime", session.RuntimeGeneration, session.Revision, true,
		time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	archived := state.Sessions[ref.Key()]
	if err := state.ReattachSessionRuntime(
		ref, archived.RuntimeGeneration, archived.Revision, time.Unix(30, 0).UTC(),
	); err != domain.ErrInvalidState {
		t.Fatalf("materialized archive reattach error=%v", err)
	}
}

func TestReattachSessionRuntimeRequiresCurrentVersionAndOnlineNode(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "guarded", "alpha", 1, time.Unix(10, 0).UTC())
	session := state.Sessions[ref.Key()]
	if err := state.ArchiveSession(
		ref, session.Revision, domain.ArchiveResumeFailed, time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	archived := state.Sessions[ref.Key()]
	if err := state.ReattachSessionRuntime(
		ref, archived.RuntimeGeneration+1, archived.Revision, time.Unix(30, 0).UTC(),
	); err != domain.ErrStaleOperation {
		t.Fatalf("stale generation error=%v", err)
	}
	if err := state.ReattachSessionRuntime(
		ref, archived.RuntimeGeneration, archived.Revision+1, time.Unix(30, 0).UTC(),
	); err != domain.ErrStaleOperation {
		t.Fatalf("stale revision error=%v", err)
	}
	node := state.Nodes[ref.NodeID]
	node.Status = domain.NodeOffline
	state.Nodes[ref.NodeID] = node
	if err := state.ReattachSessionRuntime(
		ref, archived.RuntimeGeneration, archived.Revision, time.Unix(30, 0).UTC(),
	); err != domain.ErrInvalidState {
		t.Fatalf("offline node error=%v", err)
	}
}
