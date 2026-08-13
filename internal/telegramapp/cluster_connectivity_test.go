package telegramapp

import (
	"testing"
	"time"
)

func TestConnectivityTrackerDebouncesLossAndRecovery(t *testing.T) {
	started := time.Unix(100, 0)
	grace := 10 * time.Second
	tracker := connectivityTracker{}

	if event := tracker.observe(started, false, true, grace); event != connectivityNoEvent {
		t.Fatalf("unarmed event=%d", event)
	}
	if event := tracker.observe(started.Add(time.Second), true, true, grace); event != connectivityNoEvent {
		t.Fatalf("healthy event=%d", event)
	}
	if event := tracker.observe(started.Add(2*time.Second), false, true, grace); event != connectivityNoEvent {
		t.Fatalf("early loss event=%d", event)
	}
	if event := tracker.observe(started.Add(11*time.Second), false, true, grace); event != connectivityNoEvent {
		t.Fatalf("loss before grace event=%d", event)
	}
	if event := tracker.observe(started.Add(12*time.Second), false, true, grace); event != connectivityLost {
		t.Fatalf("mature loss event=%d", event)
	}
	// A failed delivery is retried until the event is acknowledged.
	if event := tracker.observe(started.Add(13*time.Second), false, true, grace); event != connectivityLost {
		t.Fatalf("unacknowledged loss event=%d", event)
	}
	tracker.acknowledge(connectivityLost)
	if event := tracker.observe(started.Add(14*time.Second), false, true, grace); event != connectivityNoEvent {
		t.Fatalf("duplicate loss event=%d", event)
	}
	if event := tracker.observe(started.Add(15*time.Second), true, true, grace); event != connectivityRestored {
		t.Fatalf("recovery event=%d", event)
	}
	if event := tracker.observe(started.Add(16*time.Second), true, true, grace); event != connectivityRestored {
		t.Fatalf("unacknowledged recovery event=%d", event)
	}
	tracker.acknowledge(connectivityRestored)
	if event := tracker.observe(started.Add(17*time.Second), true, true, grace); event != connectivityNoEvent {
		t.Fatalf("duplicate recovery event=%d", event)
	}
}

func TestConnectivityTrackerDisabledNodeDoesNotNotify(t *testing.T) {
	now := time.Unix(200, 0)
	tracker := connectivityTracker{armed: true, lossStarted: now, lossNotified: true}
	if event := tracker.observe(now.Add(time.Minute), false, false, time.Second); event != connectivityNoEvent {
		t.Fatalf("disabled event=%d", event)
	}
	if tracker.armed || tracker.lossNotified || !tracker.lossStarted.IsZero() {
		t.Fatalf("disabled tracker=%#v", tracker)
	}
}
