package nativeapprovalflow

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
)

type flowScreenProvider struct{ snapshot sessionruntime.NativeSnapshot }

func (p flowScreenProvider) NativeScreen(domain.SessionID) (sessionruntime.NativeSnapshot, bool) {
	return p.snapshot, true
}

func (p flowScreenProvider) NativeScreenUpdates() <-chan domain.SessionID { return nil }

type flowDriver struct {
	mu       sync.Mutex
	requests []sessionruntime.NativeRequest
	result   error
	results  []error
	entered  chan struct{}
	release  chan struct{}
}

func (d *flowDriver) NativeControl(_ context.Context, _ domain.SessionID, request sessionruntime.NativeRequest) (sessionruntime.NativeSnapshot, error) {
	d.mu.Lock()
	d.requests = append(d.requests, request)
	entered, release, result := d.entered, d.release, d.result
	if len(d.results) > 0 {
		result = d.results[0]
		d.results = d.results[1:]
	}
	d.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		<-release
	}
	return sessionruntime.NativeSnapshot{}, result
}

func (d *flowDriver) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

func (d *flowDriver) last() sessionruntime.NativeRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requests[len(d.requests)-1]
}

func flowFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../nativeapproval/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func enabledAlways(context.Context) (bool, error) { return true, nil }

func TestObserveSendsOneApprovalForFullAndCollapsedScreens(t *testing.T) {
	for _, test := range []struct {
		name, fixture string
	}{
		{name: "full", fixture: "codex-command-approval.txt"},
		{name: "collapsed", fixture: "codex-command-approval-collapsed.txt"},
		{name: "wrapped persistent suffix", fixture: "codex-command-approval-p-suffix-wrapped.txt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			driver := &flowDriver{}
			flow := &Flow{}
			source := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{
				FullText: flowFixture(t, test.fixture), Hash: strings.Repeat("a", 64),
				Interactive: true, ProviderSessionID: "provider-1", Generation: 7,
			}}
			for i := 0; i < 2; i++ {
				if err := flow.Observe(context.Background(), "session-1", source, driver, enabledAlways); err != nil {
					t.Fatal(err)
				}
			}
			if got := driver.count(); got != 1 {
				t.Fatalf("NativeControl calls=%d, want 1", got)
			}
		})
	}
}

func TestObserveApprovesDistinctConsecutiveDialogsInSameGeneration(t *testing.T) {
	for _, test := range []struct {
		name, first, second string
	}{
		{name: "full then full", first: "codex-command-approval.txt", second: "codex-command-approval-wiki.txt"},
		{name: "collapsed then full", first: "codex-command-approval-collapsed.txt", second: "codex-command-approval.txt"},
		{name: "full then collapsed", first: "codex-command-approval.txt", second: "codex-command-approval-collapsed.txt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			driver := &flowDriver{}
			flow := &Flow{}
			for index, screen := range []string{flowFixture(t, test.first), flowFixture(t, test.second)} {
				source := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{
					FullText: screen, Hash: strings.Repeat(string(rune('a'+index)), 64), Interactive: true,
					ProviderSessionID: "provider-1", Generation: 7,
				}}
				if err := flow.Observe(context.Background(), "session-1", source, driver, enabledAlways); err != nil {
					t.Fatal(err)
				}
			}
			if got := driver.count(); got != 2 {
				t.Fatalf("NativeControl calls=%d, want one for each distinct consecutive dialog", got)
			}
		})
	}
}

func TestObserveOffSendsNothing(t *testing.T) {
	driver := &flowDriver{}
	flow := &Flow{}
	source := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{FullText: flowFixture(t, "codex-command-approval.txt"), Hash: strings.Repeat("b", 64), ProviderSessionID: "provider-1", Generation: 1}}
	if err := flow.Observe(context.Background(), "session-1", source, driver, func(context.Context) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if driver.count() != 0 {
		t.Fatal("disabled flow sent approval")
	}
}

func TestObserveDisabledOnSecondEnabledReadDoesNotClaim(t *testing.T) {
	driver := &flowDriver{}
	flow := &Flow{}
	source := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{FullText: flowFixture(t, "codex-command-approval.txt"), Hash: strings.Repeat("c", 64), ProviderSessionID: "provider-1", Generation: 1}}
	reads := 0
	enabled := func(context.Context) (bool, error) {
		reads++
		return reads != 2, nil
	}
	if err := flow.Observe(context.Background(), "session-1", source, driver, enabled); err != nil {
		t.Fatal(err)
	}
	if driver.count() != 0 {
		t.Fatal("disabled reread sent approval")
	}
	if err := flow.Observe(context.Background(), "session-1", source, driver, enabledAlways); err != nil {
		t.Fatal(err)
	}
	if driver.count() != 1 {
		t.Fatalf("re-enabled flow calls=%d, want 1", driver.count())
	}
}

func TestObserveUnknownResultIsNotRetried(t *testing.T) {
	driver := &flowDriver{result: errors.New("unknown outcome")}
	flow := &Flow{}
	source := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{FullText: flowFixture(t, "codex-command-approval.txt"), Hash: strings.Repeat("d", 64), ProviderSessionID: "provider-1", Generation: 1}}
	if err := flow.Observe(context.Background(), "session-1", source, driver, enabledAlways); err == nil {
		t.Fatal("unknown result was hidden")
	}
	if err := flow.Observe(context.Background(), "session-1", source, driver, enabledAlways); err != nil {
		t.Fatal(err)
	}
	if driver.count() != 1 {
		t.Fatalf("unknown result retried: calls=%d", driver.count())
	}
}

func TestToggleSerializesWithPendingDispatch(t *testing.T) {
	driver := &flowDriver{entered: make(chan struct{}), release: make(chan struct{})}
	flow := &Flow{}
	source := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{FullText: flowFixture(t, "codex-command-approval.txt"), Hash: strings.Repeat("e", 64), ProviderSessionID: "provider-1", Generation: 1}}
	observeDone := make(chan error, 1)
	go func() { observeDone <- flow.Observe(context.Background(), "session-1", source, driver, enabledAlways) }()
	select {
	case <-driver.entered:
	case <-time.After(time.Second):
		t.Fatal("dispatch did not start")
	}
	toggleDone := make(chan error, 1)
	go func() { toggleDone <- flow.Toggle(context.Background(), func(context.Context) error { return nil }) }()
	select {
	case <-toggleDone:
		t.Fatal("toggle overtook pending dispatch")
	case <-time.After(25 * time.Millisecond):
	}
	close(driver.release)
	if err := <-observeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-toggleDone; err != nil {
		t.Fatal(err)
	}
}

func TestObserveForwardsExactApprovalIdentity(t *testing.T) {
	driver := &flowDriver{}
	flow := &Flow{}
	source := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{
		FullText: flowFixture(t, "codex-command-approval.txt"), Hash: strings.Repeat("f", 64),
		ProviderSessionID: "provider-exact", Generation: 19,
	}}
	if err := flow.Observe(context.Background(), "session-1", source, driver, enabledAlways); err != nil {
		t.Fatal(err)
	}
	got := driver.last()
	if got.Key != "approve_once" || got.ExpectedHash != source.snapshot.Hash || got.ExpectedProviderSessionID != source.snapshot.ProviderSessionID || got.ExpectedGeneration != source.snapshot.Generation {
		t.Fatalf("request=%+v", got)
	}
}
