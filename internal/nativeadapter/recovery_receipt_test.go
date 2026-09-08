package nativeadapter

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/domain"
	"bria/internal/recoveryruntime"
	"bria/internal/runtimeprotocol"
	"bria/internal/sessionruntime"
)

// Exercise Run's actual protocol boundary, then independently reopen its durable
// receipt at the acceptance frame (before any final exists for this request).
func TestAcceptedFrameHasDurableNativeTurnCorrelation(t *testing.T) {
	h := startNativeFixture(t)
	if h.receive(t).Type != runtimeprotocol.TypeReady {
		t.Fatal("missing ready")
	}
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeSubmit, RequestID: "recover-request", MessageID: "recover-message", Text: "async-picker"})
	if frame := h.receive(t); frame.Type != runtimeprotocol.TypeAccepted {
		t.Fatalf("expected acceptance, got %s", frame.Type)
	}
	workdir := filepath.Dir(os.Getenv("CODEX_HOME"))
	reader, err := recoveryruntime.NewNative(filepath.Join(workdir, "state"), domain.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.ReadAcceptedTurns(context.Background(), sessionruntime.AcceptedTurnReadRequest{SessionID: "logical", Provider: domain.ProviderCodex, Workdir: workdir, Binding: domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: fixtureSession, Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 1 || got.Turns[0].MessageID != "recover-message" || got.Turns[0].TurnID != "turn-async" || got.Turns[0].Outcome != sessionruntime.AcceptedTurnUnknown {
		t.Fatalf("acceptance ack precedes durable exact-turn correlation: %#v", got)
	}
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
}
