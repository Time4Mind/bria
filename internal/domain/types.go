// Package domain contains deterministic Bria product state.
//
// It deliberately has no network, filesystem, Telegram, tmux, or clock
// dependencies. Consensus commands supply timestamps and identifiers.
package domain

import (
	"errors"
	"fmt"
	"strings"
)

type UserID int64
type NodeID string
type SessionID string

type SessionRef struct {
	NodeID    NodeID    `json:"node_id"`
	SessionID SessionID `json:"session_id"`
}

func (r SessionRef) Validate() error {
	if err := validateIdentifier("node id", string(r.NodeID)); err != nil {
		return err
	}
	return validateIdentifier("session id", string(r.SessionID))
}

func (r SessionRef) Key() string {
	return string(r.NodeID) + "/" + string(r.SessionID)
}

func validateIdentifier(label, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", label)
	}
	if len(value) > 128 {
		return fmt.Errorf("%s is too long", label)
	}
	for _, char := range value {
		if char <= 0x20 || char == '/' || char == '\\' {
			return fmt.Errorf("%s contains an unsafe character", label)
		}
	}
	return nil
}

var (
	ErrAccessDenied   = errors.New("access denied")
	ErrAlreadyExists  = errors.New("already exists")
	ErrNotFound       = errors.New("not found")
	ErrInvalidState   = errors.New("invalid state")
	ErrStaleOperation = errors.New("stale operation")
	ErrQueueFull      = errors.New("offline input queue is full")
)
