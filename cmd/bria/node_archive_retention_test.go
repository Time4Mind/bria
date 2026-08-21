package main

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

type retentionNodeStub struct {
	leader  bool
	machine *clusterstate.Machine
}

func (n *retentionNodeStub) IsLeader() bool               { return n.leader }
func (n *retentionNodeStub) State() *clusterstate.Machine { return n.machine }
func (n *retentionNodeStub) Apply(_ context.Context, command clusterstate.Command) (clusterstate.Result, error) {
	return n.machine.Apply(command), nil
}

func TestArchiveRetentionPurgesOnlyDueReadyArchive(t *testing.T) {
	now := time.Unix(2_000_000, 0).UTC()
	state := domain.NewState()
	state.Preferences[7] = domain.DefaultUserPreferences()
	due := archivedRetentionSession("due", now.Add(-15*24*time.Hour), true)
	recent := archivedRetentionSession("recent", now.Add(-time.Hour), true)
	unready := archivedRetentionSession("unready", now.Add(-15*24*time.Hour), false)
	state.Sessions[due.Ref().Key()] = due
	state.Sessions[recent.Ref().Key()] = recent
	state.Sessions[unready.Ref().Key()] = unready
	node := &retentionNodeStub{leader: true, machine: clusterstate.NewMachine(state)}

	if purged, err := reconcileArchiveRetention(context.Background(), node, now); err != nil || purged != 1 {
		t.Fatalf("purged=%d err=%v", purged, err)
	}
	got := node.machine.State()
	if _, ok := got.Sessions[due.Ref().Key()]; ok {
		t.Fatal("due archive was not purged")
	}
	if _, ok := got.SessionTombstones[due.Ref().Key()]; !ok {
		t.Fatal("purged archive has no tombstone")
	}
	if _, ok := got.Sessions[recent.Ref().Key()]; !ok {
		t.Fatal("recent archive was purged")
	}
	if _, ok := got.Sessions[unready.Ref().Key()]; !ok {
		t.Fatal("unfinished archive was purged")
	}
}

func TestArchiveRetentionFollowerDoesNothing(t *testing.T) {
	now := time.Unix(2_000_000, 0).UTC()
	state := domain.NewState()
	state.Preferences[7] = domain.DefaultUserPreferences()
	session := archivedRetentionSession("due", now.Add(-15*24*time.Hour), true)
	state.Sessions[session.Ref().Key()] = session
	node := &retentionNodeStub{machine: clusterstate.NewMachine(state)}
	if purged, err := reconcileArchiveRetention(context.Background(), node, now); err != nil || purged != 0 {
		t.Fatalf("purged=%d err=%v", purged, err)
	}
	if _, ok := node.machine.State().Sessions[session.Ref().Key()]; !ok {
		t.Fatal("follower purged archive")
	}
}

func archivedRetentionSession(id string, archivedAt time.Time, ready bool) domain.Session {
	return domain.Session{
		ID: domain.SessionID(id), NodeID: "node", OwnerID: 7, Backend: "codex",
		State: domain.SessionArchived, RuntimePhase: domain.RuntimeIdle,
		RuntimeGeneration: 3, Revision: 4, CreatedAt: archivedAt.Add(-time.Hour),
		ArchivedAt: archivedAt, ArchiveID: "archive-" + id,
		ArchiveReason: domain.ArchiveManual, ArchiveReady: ready,
	}
}

type purgeStateStub struct {
	states []*domain.State
	index  int
}

func (s *purgeStateStub) State() *domain.State {
	index := s.index
	if index >= len(s.states) {
		index = len(s.states) - 1
	}
	s.index++
	return s.states[index].Clone()
}

type purgeArchiveStub struct {
	ready   []string
	deleted []string
}

func (s *purgeArchiveStub) ReadyArchiveIDs() ([]string, error) {
	return append([]string(nil), s.ready...), nil
}
func (s *purgeArchiveStub) DeleteArchive(_ context.Context, id string) error {
	s.deleted = append(s.deleted, id)
	return nil
}

type purgeBindingStub struct{ deleted []domain.SessionRef }

func (s *purgeBindingStub) DeleteIfGeneration(ref domain.SessionRef, _ uint64) error {
	s.deleted = append(s.deleted, ref)
	return nil
}

func TestLocalArchivePurgeDeletesTombstoneAndOrphanButPreservesRacingArchive(t *testing.T) {
	tombstoneRef := domain.SessionRef{NodeID: "node", SessionID: "old"}
	first := domain.NewState()
	first.SessionTombstones[tombstoneRef.Key()] = domain.SessionTombstone{
		Session: tombstoneRef, ArchiveID: "old-bundle", RuntimeGeneration: 2,
		PurgedAt: time.Unix(100, 0).UTC(),
	}
	second := first.Clone()
	racing := archivedRetentionSession("racing", time.Unix(200, 0).UTC(), true)
	second.Sessions[racing.Ref().Key()] = racing
	archives := &purgeArchiveStub{ready: []string{"old-bundle", "orphan", racing.ArchiveID}}
	bindings := &purgeBindingStub{}
	reconciler := &localArchivePurgeReconciler{
		nodeID: "node", state: &purgeStateStub{states: []*domain.State{first, second}},
		archives: archives, bindings: bindings, cleaned: make(map[string]bool),
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(bindings.deleted) != 1 || bindings.deleted[0] != tombstoneRef {
		t.Fatalf("deleted bindings=%v", bindings.deleted)
	}
	for _, deleted := range archives.deleted {
		if deleted == racing.ArchiveID {
			t.Fatal("racing active archive was deleted")
		}
	}
	if len(archives.deleted) != 2 {
		t.Fatalf("deleted archives=%v", archives.deleted)
	}
}
