package domain_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestDeferredInputQueueIsBoundedDeduplicatedAndFIFO(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "queued", "alpha", 1, time.Unix(10, 0).UTC())
	node := state.Nodes[ref.NodeID]
	node.Status = domain.NodeOffline
	state.Nodes[ref.NodeID] = node

	for index := 1; index <= 5; index++ {
		input := deferredText(ref, fmt.Sprintf("input-%d", index), fmt.Sprintf("text-%d", index))
		queuedAt := time.Unix(int64(20+index), 0).UTC()
		if err := state.QueueDeferredSessionInput(input, queuedAt); err != nil {
			t.Fatal(err)
		}
		if got := state.DeferredInputs[ref.Key()][index-1].QueuedAt; got != queuedAt {
			t.Fatalf("queued_at=%s, want %s", got, queuedAt)
		}
	}
	tracked := state.Sessions[ref.Key()]
	if !tracked.UserRequestTracked || !tracked.UserRequestSeen {
		t.Fatalf("deferred request was not tracked: %#v", tracked)
	}
	duplicate := deferredText(ref, "input-1", "ignored duplicate")
	if err := state.QueueDeferredSessionInput(duplicate, time.Unix(30, 0).UTC()); err != nil {
		t.Fatalf("idempotent duplicate: %v", err)
	}
	if got := len(state.DeferredInputs[ref.Key()]); got != 5 {
		t.Fatalf("queue length=%d", got)
	}
	if err := state.QueueDeferredSessionInput(
		deferredText(ref, "input-6", "overflow"), time.Unix(31, 0).UTC(),
	); !errors.Is(err, domain.ErrQueueFull) {
		t.Fatalf("overflow error=%v", err)
	}
	if err := state.ResolveDeferredSessionInput(ref, "input-2", false, "", time.Unix(32, 0).UTC()); !errors.Is(err, domain.ErrStaleOperation) {
		t.Fatalf("out-of-order resolve error=%v", err)
	}
	if err := state.ResolveDeferredSessionInput(ref, "input-1", false, "", time.Unix(33, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if got := state.DeferredInputs[ref.Key()][0].OperationID; got != "input-2" {
		t.Fatalf("new head=%q", got)
	}
	clone := state.Clone()
	clone.DeferredInputs[ref.Key()][0].Text = "changed"
	if state.DeferredInputs[ref.Key()][0].Text == "changed" {
		t.Fatal("clone shares deferred queue storage")
	}
}

func TestDeferredQueueLimitPreferenceAllowsTwenty(t *testing.T) {
	state := fixtureState(t)
	preferences := state.Preferences[1]
	preferences.OfflineInputQueueLimit = 20
	if err := state.SetPreferences(1, preferences); err != nil {
		t.Fatal(err)
	}
	ref := addSession(t, state, "queued", "alpha", 1, time.Unix(10, 0).UTC())
	node := state.Nodes[ref.NodeID]
	node.Status = domain.NodeOffline
	state.Nodes[ref.NodeID] = node
	for index := 1; index <= 20; index++ {
		if err := state.QueueDeferredSessionInput(
			deferredText(ref, fmt.Sprintf("input-%d", index), "text"),
			time.Unix(int64(20+index), 0).UTC(),
		); err != nil {
			t.Fatalf("input %d: %v", index, err)
		}
	}
	if err := state.QueueDeferredSessionInput(
		deferredText(ref, "input-21", "overflow"), time.Unix(50, 0).UTC(),
	); !errors.Is(err, domain.ErrQueueFull) {
		t.Fatalf("overflow error=%v", err)
	}
}

func TestArchivingSessionDropsUndeliverableQueue(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "queued", "alpha", 1, time.Unix(10, 0).UTC())
	node := state.Nodes[ref.NodeID]
	node.Status = domain.NodeOffline
	state.Nodes[ref.NodeID] = node
	if err := state.QueueDeferredSessionInput(
		deferredText(ref, "input-1", "hello"), time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	node.Status = domain.NodeOnline
	state.Nodes[ref.NodeID] = node
	session := state.Sessions[ref.Key()]
	if err := state.ArchiveSession(ref, session.Revision, domain.ArchiveResumeFailed, time.Unix(30, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.DeferredInputs[ref.Key()]; ok {
		t.Fatal("archived session retained deferred inputs")
	}
}

func deferredText(ref domain.SessionRef, operationID, text string) domain.DeferredSessionInput {
	return domain.DeferredSessionInput{
		OperationID: operationID, ActorID: 1, Session: ref,
		ExpectedGeneration: 1, Kind: domain.DeferredInputText,
		Text: text,
	}
}
