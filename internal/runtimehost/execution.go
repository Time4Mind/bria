package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrRuntimeUnavailable       = errors.New("local runtime is unavailable")
	ErrRuntimeShuttingDown      = errors.New("local runtime is shutting down")
	ErrStaleRuntime             = errors.New("local runtime generation is stale")
	ErrOperationIDConflict      = errors.New("operation id was reused for another request")
	ErrOperationOutcomeUnknown  = errors.New("operation outcome is unknown; refusing to repeat it")
	ErrQueueFull                = errors.New("local runtime queue is full")
	ErrInputUnconfirmed         = errors.New("provider did not confirm submitted input")
	ErrTerminalUnavailable      = errors.New("local terminal is unavailable")
	ErrUnsupportedBackendAction = errors.New("backend does not support this action")
)

// ProviderConfirmationPendingDetail marks a prompt that was written to the
// provider terminal once, but did not appear in the transcript within the
// bounded confirmation window. It is a durable reconciliation marker, not an
// error and never contains prompt content.
const ProviderConfirmationPendingDetail = "provider confirmation pending"

type Action string

const (
	ActionSendInput    Action = "send_input"
	ActionStop         Action = "stop"
	ActionClear        Action = "clear"
	ActionClose        Action = "close"
	ActionDiscard      Action = "discard"
	ActionOpenTerminal Action = "open_terminal"
	ActionCapture      Action = "capture"
	ActionSendKey      Action = "send_key"
	ActionGenerateName Action = "generate_name"
)

type InteractiveKey string

const (
	KeyUp     InteractiveKey = "up"
	KeyDown   InteractiveKey = "down"
	KeyLeft   InteractiveKey = "left"
	KeyRight  InteractiveKey = "right"
	KeyEnter  InteractiveKey = "enter"
	KeyEscape InteractiveKey = "escape"
	KeySpace  InteractiveKey = "space"
	KeyTab    InteractiveKey = "tab"
	KeyCtrlC  InteractiveKey = "ctrl_c"
)

type ArchivePayload struct {
	ArchiveID         string    `json:"archive_id"`
	OwnerID           int64     `json:"owner_id"`
	Name              string    `json:"name"`
	Workdir           string    `json:"workdir"`
	ProviderSessionID string    `json:"provider_session_id"`
	CreatedAt         time.Time `json:"created_at"`
	ArchivedAt        time.Time `json:"archived_at"`
	Reason            string    `json:"reason,omitempty"`
}

// Request is the host-local execution contract. NodeID, SessionID, and
// ExpectedGeneration make a command address one exact runtime incarnation.
// Payloads are never written to replicated state.
type Request struct {
	OperationID        string          `json:"operation_id"`
	ActorID            int64           `json:"actor_id"`
	NodeID             string          `json:"node_id"`
	SessionID          string          `json:"session_id"`
	ExpectedGeneration uint64          `json:"expected_generation"`
	Action             Action          `json:"action"`
	Text               string          `json:"text,omitempty"`
	Input              *InputPayload   `json:"input,omitempty"`
	Key                InteractiveKey  `json:"key,omitempty"`
	ExpectedPromptHash string          `json:"expected_prompt_hash,omitempty"`
	Backend            string          `json:"backend"`
	ArchiveCommitID    string          `json:"archive_commit_id,omitempty"`
	Archive            *ArchivePayload `json:"archive,omitempty"`
}

type Result struct {
	Accepted             bool   `json:"accepted"`
	Delivered            bool   `json:"delivered"`
	Duplicate            bool   `json:"duplicate"`
	Detail               string `json:"detail,omitempty"`
	ResetNaming          bool   `json:"reset_naming"`
	ResetProviderBinding bool   `json:"reset_provider_binding"`
	Pane                 []byte `json:"pane,omitempty"`
	GeneratedName        string `json:"generated_name,omitempty"`
	ArchiveCommitted     bool   `json:"archive_committed,omitempty"`
	ResolvedText         string `json:"resolved_text,omitempty"`
	ProviderAccepted     *bool  `json:"provider_accepted,omitempty"`
}

type Receipt struct {
	OperationID string `json:"operation_id"`
	Accepted    bool   `json:"accepted"`
	Duplicate   bool   `json:"duplicate"`
	Detail      string `json:"detail,omitempty"`
}

type Executor interface {
	Submit(context.Context, Request) (Receipt, error)
	LookupResult(context.Context, string) (Result, bool, error)
}

func (r Request) validate() error {
	if r.ActorID <= 0 {
		return errors.New("actor id must be positive")
	}
	if strings.TrimSpace(r.NodeID) == "" || strings.TrimSpace(r.SessionID) == "" {
		return errors.New("node id and session id are required")
	}
	if r.ExpectedGeneration == 0 {
		return errors.New("expected generation must be positive")
	}
	if strings.TrimSpace(r.Backend) == "" {
		return errors.New("backend is required")
	}
	if strings.TrimSpace(r.OperationID) == "" || len(r.OperationID) > 128 {
		return errors.New("operation id must contain 1 to 128 characters")
	}
	if r.Action != ActionSendKey && (r.Key != "" || r.ExpectedPromptHash != "") {
		return fmt.Errorf("%s does not accept an interactive key", r.Action)
	}
	switch r.Action {
	case ActionSendInput:
		if (r.Text == "") == (r.Input == nil) {
			return errors.New("text is required")
		}
		if r.Input != nil {
			if err := r.Input.validate(); err != nil {
				return err
			}
		}
		if r.ArchiveCommitID != "" || r.Archive != nil {
			return fmt.Errorf("%s does not accept archive metadata", r.Action)
		}
	case ActionGenerateName:
		if r.Text == "" || r.Input != nil {
			return errors.New("text is required")
		}
		if r.ArchiveCommitID != "" || r.Archive != nil {
			return fmt.Errorf("%s does not accept archive metadata", r.Action)
		}
	case ActionStop, ActionClear, ActionDiscard, ActionOpenTerminal, ActionCapture:
		if r.Text != "" || r.Input != nil || r.ArchiveCommitID != "" || r.Archive != nil {
			return fmt.Errorf("%s does not accept a payload", r.Action)
		}
	case ActionSendKey:
		if !r.Key.valid() || len(r.ExpectedPromptHash) != 32 || r.Text != "" ||
			r.Input != nil || r.ArchiveCommitID != "" || r.Archive != nil {
			return errors.New("interactive key request is invalid")
		}
	case ActionClose:
		if strings.TrimSpace(r.ArchiveCommitID) == "" {
			return errors.New("archive commit id is required before close")
		}
		if r.Text != "" || r.Input != nil {
			return errors.New("close does not accept text")
		}
		if r.Archive == nil || r.Archive.ArchiveID != r.ArchiveCommitID ||
			r.Archive.OwnerID <= 0 || strings.TrimSpace(r.Archive.Workdir) == "" ||
			r.Archive.CreatedAt.IsZero() || r.Archive.ArchivedAt.IsZero() ||
			r.Archive.ArchivedAt.Before(r.Archive.CreatedAt) {
			return errors.New("close archive metadata is invalid")
		}
	default:
		return fmt.Errorf("unsupported runtime action: %q", r.Action)
	}
	return nil
}

func (k InteractiveKey) valid() bool {
	switch k {
	case KeyUp, KeyDown, KeyLeft, KeyRight, KeyEnter, KeyEscape, KeySpace, KeyTab, KeyCtrlC:
		return true
	default:
		return false
	}
}
