package telegramcontroller_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

type identifiedEventState struct {
	retryFinalState
	eventMu sync.Mutex
	events  map[string]string
	fail    bool
}

func (s *identifiedEventState) InsertCardRuntimeEvent(_ context.Context, _ domain.SessionID, message, id, text, kind string) error {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.fail {
		return errors.New("synthetic event save failure")
	}
	key := message + ":" + id
	if old, ok := s.events[key]; ok && old != kind+":"+text {
		return errors.New("event identity conflict")
	}
	s.events[key] = kind + ":" + text
	return nil
}

func TestAcceptedContinuationReplaysStableEventsThroughDurableHistoryAndOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "native", 2)
	running, err := ready.StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := running.Binding()
	state := &identifiedEventState{retryFinalState: retryFinalState{attempt: make(chan struct{}, 8)}, events: map[string]string{}}
	var outputMu sync.Mutex
	outputs := map[string]string{}
	custody := durableOutputFunc(func(_ context.Context, n telegramcontroller.OutgoingNotification) (telegramcontroller.OutputReceipt, error) {
		outputMu.Lock()
		defer outputMu.Unlock()
		old, exists := outputs[n.OperationID]
		if exists && old != string(n.Payload) {
			return telegramcontroller.OutputReceipt{}, errors.New("stable output identity collision")
		}
		outputs[n.OperationID] = string(n.Payload)
		return telegramcontroller.OutputReceipt{SessionID: n.SessionID, OperationID: n.OperationID, Sequence: uint64(len(outputs)), Inserted: !exists}, nil
	})
	for restart := 0; restart < 2; restart++ {
		provider := &acceptedObserver{observe: func(_ context.Context, _ domain.SessionID, _ domain.ProviderBinding, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
			for _, event := range []sessionruntime.TurnEvent{{ID: "offset:10", Kind: sessionruntime.EventCommentary, Text: "one"}, {ID: "offset:20", Kind: sessionruntime.EventCommentary, Text: "two"}, {ID: "offset:10", Kind: sessionruntime.EventCommentary, Text: "one"}} {
				if err := cb.OnEvent(event); err != nil {
					return sessionruntime.TurnResult{}, err
				}
			}
			return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "answer", Events: []sessionruntime.TurnEvent{{Kind: sessionruntime.EventCommentary, Text: "must not replay bounded result prefix"}}}, nil
		}}
		c := newController(t, nil, newLockedSessions(running), provider, nil, telegramcontroller.Options{UIState: state, DurableOutput: custody})
		done := make(chan telegramcontroller.DurableInputProcessReceipt, 1)
		if err := c.ContinueAcceptedInput(ctx, binding, telegramcontroller.DurableLeasedInput{SessionID: running.ID(), MessageID: "accepted", Sequence: 1}, telegramcontroller.DurableInputCallbacks{OnCompleted: func(_ context.Context, r telegramcontroller.DurableInputProcessReceipt) error { done <- r; return nil }}); err != nil {
			t.Fatal(err)
		}
		select {
		case r := <-done:
			if r.Completion != telegramcontroller.DurableInputSucceeded {
				t.Fatalf("continuation failed: %+v", r)
			}
		case <-ctx.Done():
			t.Fatal("continuation did not finish")
		}
		if err := c.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	state.eventMu.Lock()
	count := len(state.events)
	state.eventMu.Unlock()
	if count != 2 {
		t.Fatalf("durable identified events=%d want=2", count)
	}
	outputMu.Lock()
	defer outputMu.Unlock()
	if len(outputs) != 3 {
		t.Fatalf("stable event/final outputs=%d want=3", len(outputs))
	}
	if outputs["accepted:final"] != "answer" {
		t.Fatal("final identity changed")
	}
}

func TestAppliedSteerOwnsItsEventsAndFinalThroughController(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := readySession(t, "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb", domain.ProviderCodex, t.TempDir(), "native", 2)
	running, err := ready.StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := running.Binding()
	state := &identifiedEventState{retryFinalState: retryFinalState{attempt: make(chan struct{}, 4)}, events: map[string]string{}}
	outputs := map[string]string{}
	provider := &acceptedObserver{observe: func(_ context.Context, _ domain.SessionID, _ domain.ProviderBinding, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		if err := cb.OnEvent(sessionruntime.TurnEvent{ID: "offset:30", Kind: sessionruntime.EventCommentary, Text: "after steer", MessageID: "steer"}); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "steer final", FinalMessageID: "steer"}, nil
	}}
	c := newController(t, nil, newLockedSessions(running), provider, nil, telegramcontroller.Options{
		UIState: state,
		DurableOutput: durableOutputFunc(func(_ context.Context, n telegramcontroller.OutgoingNotification) (telegramcontroller.OutputReceipt, error) {
			outputs[n.OperationID] = string(n.Payload)
			return telegramcontroller.OutputReceipt{SessionID: n.SessionID, OperationID: n.OperationID, Sequence: uint64(len(outputs))}, nil
		}),
	})
	defer c.Close(context.Background())
	done := make(chan telegramcontroller.DurableInputProcessReceipt, 1)
	if err := c.ContinueAcceptedInput(ctx, binding, telegramcontroller.DurableLeasedInput{SessionID: running.ID(), MessageID: "root", Sequence: 1}, telegramcontroller.DurableInputCallbacks{OnCompleted: func(_ context.Context, r telegramcontroller.DurableInputProcessReceipt) error {
		done <- r
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case receipt := <-done:
		if receipt.Completion != telegramcontroller.DurableInputSucceeded {
			t.Fatalf("completion = %+v", receipt)
		}
	case <-ctx.Done():
		t.Fatal("accepted turn did not finish")
	}
	state.eventMu.Lock()
	event := state.events["steer:offset:30"]
	state.eventMu.Unlock()
	if event != "commentary:after steer" || outputs["steer:final"] != "steer final" {
		t.Fatalf("owner routing: event=%q outputs=%v", event, outputs)
	}
}
