package sessionruntime

import (
	"context"
	"errors"
	"testing"

	"bria/internal/domain"
)

type rejectPrematureWrite struct{ writes int }

func (w *rejectPrematureWrite) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("premature wire write")
}
func (*rejectPrematureWrite) Close() error { return nil }

func TestCurrentInputCannotOvertakeUnsentRoot(t *testing.T) {
	wire := &rejectPrematureWrite{}
	id := domain.SessionID("pending-wire")
	turn := &activeTurn{requestID: "root", done: make(chan struct{}), steers: make(map[string]*steerWaiter)}
	starter := &Starter{maxTextBytes: 1024, maxLineBytes: 4096, processes: map[domain.SessionID]*processRecord{id: {stdin: wire, done: make(chan struct{}), turn: turn}}}
	err := starter.SubmitCurrentWithCallbacks(context.Background(), id, StructuredInput{Text: "steer"}, TurnCallbacks{MessageID: "follow-up", OnAccepted: func(string) error { t.Fatal("unsent input accepted"); return nil }})
	if !errors.Is(err, ErrNoTurnInFlight) || wire.writes != 0 {
		t.Fatalf("premature steer: error=%v writes=%d", err, wire.writes)
	}
	if err := starter.StopCurrent(context.Background(), id); !errors.Is(err, ErrNoTurnInFlight) || wire.writes != 0 {
		t.Fatalf("premature interrupt: error=%v writes=%d", err, wire.writes)
	}
}
