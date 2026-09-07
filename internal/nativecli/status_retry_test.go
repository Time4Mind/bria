package nativecli

import (
	"bria/internal/domain"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type retryTerminal struct {
	fakeTerminal
	sentAt       time.Time
	enterAt      time.Time
	neverRespond bool
}

func (t *retryTerminal) Capture(context.Context) (string, error) {
	if len(t.inputs) == 0 {
		return "›\n? for shortcuts", nil
	}
	if len(t.keys) > 0 && !t.neverRespond {
		return "Session: " + testID, nil
	}
	return "› /status \n? for shortcuts", nil
}
func (t *retryTerminal) Input(ctx context.Context, s string) error {
	t.sentAt = time.Now()
	return t.fakeTerminal.Input(ctx, s)
}
func (t *retryTerminal) Key(ctx context.Context, s string) error {
	if s == "Enter" {
		t.enterAt = time.Now()
	}
	return t.fakeTerminal.Key(ctx, s)
}

func TestReadyRetriesOnlyStatusEnterOnceWithoutRepaste(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	terminal := &retryTerminal{}
	state, err := Ready(ctx, terminal, Plan{Provider: domain.ProviderCodex})
	if err != nil || state.SessionID != testID || !reflect.DeepEqual(terminal.inputs, []string{"/status"}) || !reflect.DeepEqual(terminal.keys, []string{"Enter", "Escape"}) {
		t.Fatalf("state=%#v keys=%v inputs=%v err=%v", state, terminal.keys, terminal.inputs, err)
	}
	if terminal.enterAt.Sub(terminal.sentAt) < time.Second {
		t.Fatal("retried before one second")
	}
}

func TestReadyStatusRetryDoesNotLoopOrExtendDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1300*time.Millisecond)
	defer cancel()
	terminal := &retryTerminal{neverRespond: true}
	_, err := Ready(ctx, terminal, Plan{Provider: domain.ProviderCodex})
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(terminal.inputs, []string{"/status"}) || !reflect.DeepEqual(terminal.keys, []string{"Enter"}) {
		t.Fatalf("keys=%v inputs=%v err=%v", terminal.keys, terminal.inputs, err)
	}
}

func TestStatusRetryRequiresExactIdleBottomDraft(t *testing.T) {
	for _, text := range []string{"› /status extra\n? for shortcuts", "› /model\n? for shortcuts", "› /status\nhistorical assistant response", "Working • esc to interrupt\n› /status\n? for shortcuts", "Starting MCP servers\n› /status\n? for shortcuts", "› /status\ntab to queue message", "› /status\n› different"} {
		if statusDraftRetryEligible(text) {
			t.Errorf("unsafe retry eligible: %q", text)
		}
	}
	if !statusDraftRetryEligible("› /status \n? for shortcuts") {
		t.Fatal("exact idle draft rejected")
	}
}
