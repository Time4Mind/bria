package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/sessiondescription"
	"github.com/Time4Mind/bria/internal/transcript"
)

type descriptionServiceStub struct {
	result   sessiondescription.Result
	err      error
	requests []sessiondescription.Request
}

func (s *descriptionServiceStub) Generate(
	_ context.Context,
	request sessiondescription.Request,
) (sessiondescription.Result, error) {
	s.requests = append(s.requests, request)
	return s.result, s.err
}

func TestArchiveDescriptionReconcilerPersistsNewestMissingDescriptionOnce(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	state := domain.NewState()
	older := archivedRetentionSession("older", now.Add(-2*time.Hour), true)
	newer := archivedRetentionSession("newer", now.Add(-time.Hour), true)
	state.Sessions[older.Ref().Key()] = older
	state.Sessions[newer.Ref().Key()] = newer
	node := &retentionNodeStub{leader: true, machine: clusterstate.NewMachine(state)}
	descriptions := &descriptionServiceStub{result: sessiondescription.Result{
		Lines: []string{"Контекст.", "Результат."},
	}}
	reconciler := newArchiveDescriptionReconciler(node, descriptions)
	reconciler.now = func() time.Time { return now }

	changed, err := reconciler.reconcile(context.Background())
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	if len(descriptions.requests) != 1 || descriptions.requests[0].Session != newer.Ref() {
		t.Fatalf("requests=%#v", descriptions.requests)
	}
	got := node.machine.State().Sessions[newer.Ref().Key()]
	if got.DescriptionVersion != domain.ArchiveDescriptionVersion ||
		len(got.ArchiveDescription) != 2 {
		t.Fatalf("described session=%#v", got)
	}
	if _, err := reconciler.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(descriptions.requests) != 2 || descriptions.requests[1].Session != older.Ref() {
		t.Fatalf("second request=%#v", descriptions.requests)
	}
}

func TestArchiveDescriptionReconcilerFollowerAndRetryBackoffDoNotCallModel(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	state := domain.NewState()
	session := archivedRetentionSession("missing", now.Add(-time.Hour), true)
	state.Sessions[session.Ref().Key()] = session
	descriptions := &descriptionServiceStub{err: errors.New("unavailable")}
	node := &retentionNodeStub{machine: clusterstate.NewMachine(state)}
	reconciler := newArchiveDescriptionReconciler(node, descriptions)
	reconciler.now = func() time.Time { return now }
	if changed, err := reconciler.reconcile(context.Background()); err != nil || changed {
		t.Fatalf("follower changed=%t err=%v", changed, err)
	}
	if len(descriptions.requests) != 0 {
		t.Fatal("follower called description model")
	}
	node.leader = true
	if _, err := reconciler.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(descriptions.requests) != 1 {
		t.Fatalf("backoff requests=%d", len(descriptions.requests))
	}
}

func TestArchiveDescriptionReconcilerIncludesUnreadyAutomaticArchive(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	state := domain.NewState()
	session := archivedRetentionSession("automatic", now.Add(-time.Hour), false)
	session.ArchiveID = ""
	state.Sessions[session.Ref().Key()] = session
	node := &retentionNodeStub{leader: true, machine: clusterstate.NewMachine(state)}
	descriptions := &descriptionServiceStub{result: sessiondescription.Result{
		Lines: []string{"Контекст.", "Результат."},
	}}
	reconciler := newArchiveDescriptionReconciler(node, descriptions)
	reconciler.now = func() time.Time { return now }
	changed, err := reconciler.reconcile(context.Background())
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	if len(descriptions.requests) != 1 || descriptions.requests[0].ArchiveID != "" {
		t.Fatalf("requests=%#v", descriptions.requests)
	}
	if got := node.machine.State().Sessions[session.Ref().Key()]; got.DescriptionVersion != domain.ArchiveDescriptionVersion {
		t.Fatalf("description version=%d", got.DescriptionVersion)
	}
}

func TestArchiveDescriptionReconcilerPurgesProvenLegacyEmptyArchive(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	state := domain.NewState()
	session := archivedRetentionSession("empty-legacy", now.Add(-time.Hour), true)
	state.Sessions[session.Ref().Key()] = session
	node := &retentionNodeStub{leader: true, machine: clusterstate.NewMachine(state)}
	descriptions := &descriptionServiceStub{result: sessiondescription.Result{Empty: true}}
	reconciler := newArchiveDescriptionReconciler(node, descriptions)
	reconciler.now = func() time.Time { return now }

	changed, err := reconciler.reconcile(context.Background())
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	current := node.machine.State()
	if _, ok := current.Sessions[session.Ref().Key()]; ok {
		t.Fatal("empty archived session remained")
	}
	if tombstone, ok := current.SessionTombstones[session.Ref().Key()]; !ok || tombstone.ArchiveID != session.ArchiveID {
		t.Fatalf("tombstone=%#v present=%t", tombstone, ok)
	}
}

func TestArchiveDescriptionReconcilerBacksOffMissingPromptSourceForSixHours(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	state := domain.NewState()
	session := archivedRetentionSession("empty-legacy", now.Add(-time.Hour), true)
	state.Sessions[session.Ref().Key()] = session
	node := &retentionNodeStub{leader: true, machine: clusterstate.NewMachine(state)}
	descriptions := &descriptionServiceStub{err: transcript.ErrTranscriptNotFound}
	reconciler := newArchiveDescriptionReconciler(node, descriptions)
	reconciler.now = func() time.Time { return now }
	if _, err := reconciler.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	key := session.Ref().Key() + "\x00" + session.ArchiveID
	if got := reconciler.retryAt[key]; !got.Equal(now.Add(archiveDescriptionNoSource)) {
		t.Fatalf("retry_at=%s", got)
	}
}
