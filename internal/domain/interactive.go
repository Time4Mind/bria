package domain

import (
	"fmt"
	"strings"
	"time"
)

// InteractivePrompt is the bounded, replicated description of a host-local
// terminal prompt. The terminal contents remain on the origin node.
type InteractivePrompt struct {
	Kind       string    `json:"kind"`
	Hash       string    `json:"hash"`
	DetectedAt time.Time `json:"detected_at"`
}

type InteractivePromptReport struct {
	SessionID  SessionID `json:"session_id"`
	Generation uint64    `json:"generation"`
	Present    bool      `json:"present"`
	Kind       string    `json:"kind"`
	Hash       string    `json:"hash"`
}

func (p InteractivePromptReport) validate() error {
	if err := validateIdentifier("session id", string(p.SessionID)); err != nil {
		return err
	}
	if p.Generation == 0 {
		return fmt.Errorf("interactive prompt generation must be positive")
	}
	if !p.Present {
		if p.Kind != "" || p.Hash != "" {
			return fmt.Errorf("absent interactive prompt contains metadata")
		}
		return nil
	}
	if strings.TrimSpace(p.Kind) == "" || len(p.Kind) > 32 {
		return fmt.Errorf("interactive prompt kind is invalid")
	}
	if len(p.Hash) != 32 || strings.IndexFunc(p.Hash, func(r rune) bool {
		return !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f')
	}) >= 0 {
		return fmt.Errorf("interactive prompt hash is invalid")
	}
	return nil
}
