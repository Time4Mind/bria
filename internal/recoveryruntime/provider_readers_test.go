package recoveryruntime_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/recoveryruntime"
	"bria/internal/sessionruntime"
)

func TestProviderReadersRouteExactCodexAndClaudeBindings(t *testing.T) {
	codex := &recordingAcceptedReader{result: sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{{MessageID: "codex-message", Outcome: sessionruntime.AcceptedTurnCompleted}}}}
	claude := &recordingAcceptedReader{result: sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{{MessageID: "claude-message", Outcome: sessionruntime.AcceptedTurnUnknown}}}}
	readers, err := recoveryruntime.NewProviderReaders(codex, claude)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		provider domain.Provider
		message  string
	}{
		{provider: domain.ProviderCodex, message: "codex-message"},
		{provider: domain.ProviderClaude, message: "claude-message"},
	} {
		request := sessionruntime.AcceptedTurnReadRequest{
			SessionID: domain.SessionID("logical-" + string(test.provider)), Provider: test.provider,
			Workdir: "/work", Binding: domain.ProviderBinding{Provider: test.provider, SessionID: "provider-" + string(test.provider), Generation: 4},
		}
		result, err := readers.ReadAcceptedTurns(context.Background(), request)
		if err != nil {
			t.Fatalf("ReadAcceptedTurns(%s): %v", test.provider, err)
		}
		if len(result.Turns) != 1 || result.Turns[0].MessageID != test.message {
			t.Fatalf("ReadAcceptedTurns(%s) = %#v", test.provider, result)
		}
	}
	if len(codex.requests) != 1 || codex.requests[0].Provider != domain.ProviderCodex || len(claude.requests) != 1 || claude.requests[0].Provider != domain.ProviderClaude {
		t.Fatalf("routed requests: codex=%#v claude=%#v", codex.requests, claude.requests)
	}
	result, err := readers.ReadAcceptedTurns(context.Background(), codex.requests[0])
	if err != nil {
		t.Fatal(err)
	}
	result.Turns[0].MessageID = "mutated"
	again, err := readers.ReadAcceptedTurns(context.Background(), codex.requests[0])
	if err != nil || again.Turns[0].MessageID != "codex-message" {
		t.Fatalf("reader result was mutable: %#v, %v", again, err)
	}
}

func TestProviderReadersFailClosedForMissingAmbiguousOrMismatchedProvider(t *testing.T) {
	codex := &recordingAcceptedReader{}
	claude := &recordingAcceptedReader{}
	var typedNil *recordingAcceptedReader
	if _, err := recoveryruntime.NewProviderReaders(typedNil, claude); !errors.Is(err, recoveryruntime.ErrUnavailable) {
		t.Fatalf("typed-nil reader error = %v", err)
	}
	readers, err := recoveryruntime.NewProviderReaders(codex, claude)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []sessionruntime.AcceptedTurnReadRequest{
		{SessionID: "missing", Provider: "", Binding: domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider", Generation: 1}},
		{SessionID: "ambiguous", Provider: "future", Binding: domain.ProviderBinding{Provider: "future", SessionID: "provider", Generation: 1}},
		{SessionID: "mismatch", Provider: domain.ProviderCodex, Binding: domain.ProviderBinding{Provider: domain.ProviderClaude, SessionID: "provider", Generation: 1}},
	} {
		if _, err := readers.ReadAcceptedTurns(context.Background(), request); !errors.Is(err, recoveryruntime.ErrUnavailable) {
			t.Fatalf("request=%#v error=%v", request, err)
		}
	}
	if !reflect.DeepEqual(codex.requests, []sessionruntime.AcceptedTurnReadRequest(nil)) || !reflect.DeepEqual(claude.requests, []sessionruntime.AcceptedTurnReadRequest(nil)) {
		t.Fatalf("invalid request reached readers: codex=%#v claude=%#v", codex.requests, claude.requests)
	}
	leaking := &recordingAcceptedReader{err: errors.New("provider failed with secret-value")}
	readers, err = recoveryruntime.NewProviderReaders(leaking, claude)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readers.ReadAcceptedTurns(context.Background(), sessionruntime.AcceptedTurnReadRequest{
		SessionID: "valid", Provider: domain.ProviderCodex, Workdir: "/work",
		Binding: domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider", Generation: 1},
	})
	if !errors.Is(err, recoveryruntime.ErrUnavailable) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("reader error was not sanitized: %v", err)
	}
}

type recordingAcceptedReader struct {
	requests []sessionruntime.AcceptedTurnReadRequest
	result   sessionruntime.AcceptedTurnReconciliation
	err      error
}

func (reader *recordingAcceptedReader) ReadAcceptedTurns(_ context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	reader.requests = append(reader.requests, request)
	return reader.result, reader.err
}
