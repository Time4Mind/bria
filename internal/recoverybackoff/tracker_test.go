package recoverybackoff_test

import (
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/recoverybackoff"
)

func TestTrackerBacksOffIndefinitelyAndCapsDelay(t *testing.T) {
	tracker, err := recoverybackoff.New(time.Minute, 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000, 0).UTC()
	identity := recoverybackoff.Identity{Lifecycle: now}
	if !tracker.Ready("session", identity, now) {
		t.Fatal("first recovery attempt must be immediate")
	}

	delays := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 20 * time.Minute, 20 * time.Minute,
	}
	for _, delay := range delays {
		tracker.Failed("session", identity, now)
		if tracker.Ready("session", identity, now.Add(delay-time.Nanosecond)) {
			t.Fatalf("recovery became ready before %s elapsed", delay)
		}
		now = now.Add(delay)
		if !tracker.Ready("session", identity, now) {
			t.Fatalf("recovery is not ready after %s elapsed", delay)
		}
	}
}

func TestTrackerResetsForBindingLifecycleRemovalAndSuccess(t *testing.T) {
	tracker, err := recoverybackoff.New(time.Minute, 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000, 0).UTC()
	first := recoverybackoff.Identity{Lifecycle: now}
	tracker.Failed("session", first, now)

	changedLifecycle := first
	changedLifecycle.Lifecycle = now.Add(time.Second)
	if !tracker.Ready("session", changedLifecycle, now) {
		t.Fatal("lifecycle change must reset backoff")
	}
	tracker.Failed("session", changedLifecycle, now)
	changedBinding := changedLifecycle
	changedBinding.Binding.Generation++
	if !tracker.Ready("session", changedBinding, now) {
		t.Fatal("binding change must reset backoff")
	}

	tracker.Failed("session", changedBinding, now)
	tracker.Retain(map[domain.SessionID]recoverybackoff.Identity{})
	if !tracker.Ready("session", changedBinding, now) {
		t.Fatal("lifecycle removal must reset backoff")
	}
	tracker.Failed("session", changedBinding, now)
	tracker.Succeeded("session")
	if !tracker.Ready("session", changedBinding, now) {
		t.Fatal("successful recovery must reset backoff")
	}
}

func TestTrackerStableBarrierReleasesOnlyForChangedEvidenceOrIdentity(t *testing.T) {
	tracker, err := recoverybackoff.New(time.Minute, 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000, 0).UTC()
	identity := recoverybackoff.Identity{Lifecycle: now}
	tracker.BlockUntilEvidenceChanges("session", identity, "evidence-v1")
	if tracker.Ready("session", identity, now.Add(24*time.Hour)) {
		t.Fatal("stable barrier became retryable with elapsed time")
	}
	if revision, blocked := tracker.StableBarrier("session", identity); !blocked || revision != "evidence-v1" {
		t.Fatalf("stable barrier = %q, %t", revision, blocked)
	}
	if tracker.ReleaseIfEvidenceChanged("session", identity, "evidence-v1") || tracker.Ready("session", identity, now) {
		t.Fatal("unchanged evidence released stable barrier")
	}
	if !tracker.ReleaseIfEvidenceChanged("session", identity, "evidence-v2") || !tracker.Ready("session", identity, now) {
		t.Fatal("changed evidence did not release stable barrier")
	}

	tracker.BlockUntilEvidenceChanges("session", identity, "evidence-v2")
	changed := identity
	changed.Lifecycle = changed.Lifecycle.Add(time.Second)
	if !tracker.Ready("session", changed, now) {
		t.Fatal("changed session identity did not release stable barrier")
	}
}

func TestNewRejectsInvalidPolicy(t *testing.T) {
	for _, policy := range []struct{ base, maximum time.Duration }{
		{base: 0, maximum: time.Minute},
		{base: time.Minute, maximum: 0},
		{base: -time.Second, maximum: time.Minute},
		{base: 2 * time.Minute, maximum: time.Minute},
	} {
		if _, err := recoverybackoff.New(policy.base, policy.maximum); err == nil {
			t.Fatalf("New(%s, %s) succeeded", policy.base, policy.maximum)
		}
	}
}
