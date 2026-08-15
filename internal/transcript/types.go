// Package transcript reads backend-owned, node-local JSONL transcripts.
//
// It deliberately accepts provider session identifiers instead of paths. Raw
// transcript contents must remain on the node that owns the provider runtime;
// callers may project the returned bounded events, but must not replicate the
// source JSONL through cluster state.
package transcript

import (
	"errors"
	"fmt"
	"strings"
)

type Backend string

const (
	BackendClaude Backend = "claude"
	BackendCodex  Backend = "codex"
)

type EventKind string

const (
	EventUserText       EventKind = "user_text"
	EventAssistantText  EventKind = "assistant_text"
	EventAssistantFinal EventKind = "assistant_final"
	EventThinking       EventKind = "thinking"
	EventToolCall       EventKind = "tool_call"
	EventToolResult     EventKind = "tool_result"
)

type Event struct {
	Kind           EventKind `json:"kind"`
	Text           string    `json:"text,omitempty"`
	ToolUseID      string    `json:"tool_use_id,omitempty"`
	ToolName       string    `json:"tool_name,omitempty"`
	Head           string    `json:"head,omitempty"`
	Body           string    `json:"body,omitempty"`
	Error          bool      `json:"error,omitempty"`
	Timestamp      string    `json:"timestamp,omitempty"`
	ContextPercent *int      `json:"context_percent,omitempty"`
}

type Request struct {
	Backend           Backend
	ProviderSessionID string
	Workdir           string
}

type Config struct {
	ClaudeProjectsRoot string
	CodexSessionsRoot  string
	MaxEvents          int
	MaxReadBytes       int64
	MaxLineBytes       int
	MaxBodyBytes       int
	MaxCodexFiles      int
}

const (
	defaultMaxEvents     = 200
	defaultMaxReadBytes  = 4 << 20
	defaultMaxLineBytes  = 1 << 20
	defaultMaxBodyBytes  = 256 << 10
	defaultMaxCodexFiles = 4096
)

var (
	ErrInvalidRequest     = errors.New("invalid transcript request")
	ErrUnsupportedBackend = errors.New("unsupported transcript backend")
	ErrTranscriptNotFound = errors.New("transcript not found")
	ErrUnsafeTranscript   = errors.New("unsafe transcript path")
)

func (c Config) normalized() (Config, error) {
	if strings.TrimSpace(c.ClaudeProjectsRoot) == "" || strings.TrimSpace(c.CodexSessionsRoot) == "" {
		return Config{}, fmt.Errorf("%w: both transcript roots are required", ErrInvalidRequest)
	}
	if c.MaxEvents == 0 {
		c.MaxEvents = defaultMaxEvents
	}
	if c.MaxReadBytes == 0 {
		c.MaxReadBytes = defaultMaxReadBytes
	}
	if c.MaxLineBytes == 0 {
		c.MaxLineBytes = defaultMaxLineBytes
	}
	if c.MaxBodyBytes == 0 {
		c.MaxBodyBytes = defaultMaxBodyBytes
	}
	if c.MaxCodexFiles == 0 {
		c.MaxCodexFiles = defaultMaxCodexFiles
	}
	if c.MaxEvents < 1 || c.MaxReadBytes < 1 || c.MaxLineBytes < 1 ||
		c.MaxBodyBytes < 1 || c.MaxCodexFiles < 1 {
		return Config{}, fmt.Errorf("%w: transcript limits must be positive", ErrInvalidRequest)
	}
	return c, nil
}
