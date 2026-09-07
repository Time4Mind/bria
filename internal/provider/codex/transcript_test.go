package codex

import (
	"encoding/json"
	"testing"
)

func TestDecodeTranscriptItemPreservesReasoningAndToolLifecycle(t *testing.T) {
	reasoning, ok := decodeTranscriptItem("item/completed", json.RawMessage(`{
		"threadId":"thread-1","turnId":"turn-1",
		"item":{"type":"reasoning","id":"reason-1","summary":["inspect state"]}
	}`), 32<<10)
	if !ok || reasoning.Kind != "thinking" || reasoning.Text != "inspect state" {
		t.Fatalf("reasoning = %#v, %v", reasoning, ok)
	}

	started, ok := decodeTranscriptItem("item/started", json.RawMessage(`{
		"threadId":"thread-1","turnId":"turn-1",
		"item":{"type":"commandExecution","id":"cmd-1","command":"go test ./...","cwd":"/work","status":"inProgress"}
	}`), 32<<10)
	if !ok || started.Kind != "tool" || started.Text != "command" || started.Metadata == nil ||
		started.Metadata.ItemID != "cmd-1" || started.Metadata.Arguments != "go test ./...\n/work" || started.Metadata.Status != "inProgress" {
		t.Fatalf("started tool = %#v, %v", started, ok)
	}

	completed, ok := decodeTranscriptItem("item/completed", json.RawMessage(`{
		"threadId":"thread-1","turnId":"turn-1",
		"item":{"type":"commandExecution","id":"cmd-1","command":"go test ./...","cwd":"/work","status":"completed","aggregatedOutput":"ok"}
	}`), 32<<10)
	if !ok || completed.Metadata == nil || completed.Metadata.ItemID != "cmd-1" || completed.Metadata.Result != "ok" || completed.Metadata.Status != "completed" {
		t.Fatalf("completed tool = %#v, %v", completed, ok)
	}
}
