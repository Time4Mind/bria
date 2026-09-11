// Package turnstream owns provider-neutral streamed turn events and results.
package turnstream

import "bria/internal/runtimeprotocol"

type EventKind string

const (
	EventCommentary EventKind = "commentary"
	EventQuestion   EventKind = "question"
	EventTool       EventKind = "tool"
	EventThinking   EventKind = "thinking"
)

// Event is one ordered, safe-to-display non-final provider event.
type Event struct {
	ID        string
	Kind      EventKind
	Text      string
	Metadata  *runtimeprotocol.EventMetadata
	MessageID string
}

// Result is publishable only after a successful correlated terminal response.
type Result struct {
	Events              []Event
	Final               string
	FinalMessageID      string
	TerminalStatus      string
	ErrorCode           string
	ProviderSessionName string
}
