package sessionruntime_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/runtimeprotocol"
	"bria/internal/sessionruntime"
)

func TestCurrentTurnRejectsModelOverrideWithoutWriting(t *testing.T) {
	var starter sessionruntime.Starter
	for _, input := range []sessionruntime.StructuredInput{{Text: "hello", Model: "available-model"}, {Text: "hello", Effort: "high"}} {
		if err := starter.SubmitCurrentWithCallbacks(context.Background(), "session", input, sessionruntime.TurnCallbacks{}); !errors.Is(err, runtimeprotocol.ErrProtocol) {
			t.Fatalf("current-turn override = %v", err)
		}
	}
}
