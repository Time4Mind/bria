package finalpersist_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/domain"
	"bria/internal/finalpersist"
)

type finalSession struct{ value domain.Session }

func (s finalSession) Load(context.Context, domain.SessionID) (domain.Session, error) {
	return s.value, nil
}

type finalWriter struct{ saved bool }

func (w *finalWriter) RestoreAcceptedFinal(context.Context, domain.SessionID, string, string) error {
	w.saved = true
	return nil
}

func TestStaleFinalCannotWriteIntoNewProviderGeneration(t *testing.T) {
	starting, err := domain.NewStartingSession("s", "intent", "local", domain.ProviderCodex, "/work")
	if err != nil {
		t.Fatal(err)
	}
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "native", Generation: 2}
	ready, err := starting.Ready(binding)
	if err != nil {
		t.Fatal(err)
	}
	old := binding
	old.Generation--
	writer := &finalWriter{}
	err = finalpersist.Save(context.Background(), finalpersist.Request{SessionID: ready.ID(), Binding: old, MessageID: "old", Text: "old final"}, finalSession{ready}, writer, nil)
	if !errors.Is(err, finalpersist.ErrSuperseded) || writer.saved {
		t.Fatalf("stale final wrote=%v err=%v", writer.saved, err)
	}
}
