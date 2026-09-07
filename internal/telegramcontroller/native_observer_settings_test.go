package telegramcontroller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
)

type screenObserverPreferences struct {
	Preferences
	enabled    bool
	failure    error
	controller *Controller
}

func (p screenObserverPreferences) Snapshot(context.Context) (PreferenceSnapshot, error) {
	// This read takes c.mu; querying settings while holding c.mu would deadlock.
	_ = p.controller.NativeScreenVisible("session")
	return PreferenceSnapshot{ScreenEnabled: p.enabled}, p.failure
}

type screenObserverReader struct{ snapshot sessionruntime.NativeSnapshot }

func (r screenObserverReader) NativeScreen(domain.SessionID) (sessionruntime.NativeSnapshot, bool) {
	return r.snapshot, true
}
func (r screenObserverReader) NativeScreenUpdates() <-chan domain.SessionID { return nil }

type screenObserverNotifier struct{ notifications []Notification }

func (n *screenObserverNotifier) Notify(_ context.Context, value Notification) error {
	n.notifications = append(n.notifications, value)
	return nil
}

func TestNativeObserverDoesNotPublishScreenshotOnlyChanges(t *testing.T) {
	for _, test := range []struct {
		name                              string
		setting, enabled, visible, failed bool
		want                              int
	}{
		{"enabled", true, true, true, false, 0},
		{"disabled", true, false, true, false, 0},
		{"unset", false, false, true, false, 0},
		{"menu", true, true, false, false, 0},
		{"settings unavailable", true, true, true, true, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			notifier := &screenObserverNotifier{}
			c := &Controller{active: "session", nativeCardVisible: test.visible, nativeSnapshots: map[domain.SessionID]sessionruntime.NativeSnapshot{"session": {Hash: strings.Repeat("a", 64)}}, notifier: notifier}
			if test.setting {
				var failure error
				if test.failed {
					failure = errors.New("settings unavailable")
				}
				c.settings = screenObserverPreferences{enabled: test.enabled, failure: failure, controller: c}
			}
			reader := screenObserverReader{snapshot: sessionruntime.NativeSnapshot{Text: "ordinary terminal changed", Hash: strings.Repeat("b", 64)}}
			c.refreshNativeObservation(context.Background(), reader)
			if len(notifier.notifications) != test.want {
				t.Fatalf("ordinary changed notifications=%d want%d", len(notifier.notifications), test.want)
			}
			c.refreshNativeObservation(context.Background(), reader)
			if len(notifier.notifications) != test.want {
				t.Fatal("identical snapshot notified twice")
			}
			if test.visible && !test.failed {
				reader.snapshot.Interactive = true
				reader.snapshot.Hash = strings.Repeat("c", 64)
				c.refreshNativeObservation(context.Background(), reader)
				if len(notifier.notifications) != test.want+1 {
					t.Fatal("Screen setting suppressed interactive picker")
				}
			}
		})
	}
}
