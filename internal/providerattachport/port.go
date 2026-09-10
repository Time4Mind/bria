// Package providerattachport defines exact provider process requests and attach-only capability.
package providerattachport

import (
	"bria/internal/domain"
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrTerminalUnavailable proves that the exact persisted native terminal no
// longer exists. Temporary adapter, authentication and protocol failures must
// not be wrapped with this sentinel.
var ErrTerminalUnavailable = errors.New("exact native terminal is unavailable")

// SessionStartMode distinguishes creation of a provider session from exact
// continuation of an already identified provider session.
type SessionStartMode string

const (
	SessionStartNew    SessionStartMode = "new"
	SessionStartResume SessionStartMode = "resume"
)

// StartSessionRequest is the provider-neutral request for one local process.
type StartSessionRequest struct {
	SessionID    domain.SessionID
	ComputerID   domain.ComputerID
	Provider     domain.Provider
	Workdir      string
	Mode         SessionStartMode
	PriorBinding *domain.ProviderBinding
}

// Validate rejects ambiguous launches so an exact resume can never silently
// fall back to creation of a replacement provider session.
func (request StartSessionRequest) Validate() error {
	if strings.TrimSpace(string(request.SessionID)) == "" {
		return errors.New("logical session id is required")
	}
	if strings.TrimSpace(string(request.ComputerID)) == "" {
		return errors.New("computer id is required")
	}
	if request.Provider != domain.ProviderCodex && request.Provider != domain.ProviderClaude {
		return fmt.Errorf("unsupported provider %q", request.Provider)
	}
	if strings.TrimSpace(request.Workdir) == "" {
		return errors.New("workdir is required")
	}
	switch request.Mode {
	case SessionStartNew:
		if request.PriorBinding != nil {
			return errors.New("new session start cannot carry a prior provider binding")
		}
	case SessionStartResume:
		if request.PriorBinding == nil {
			return errors.New("exact resume requires the prior provider binding")
		}
		if request.PriorBinding.Provider != request.Provider {
			return fmt.Errorf("prior provider %q does not match requested provider %q", request.PriorBinding.Provider, request.Provider)
		}
		if strings.TrimSpace(request.PriorBinding.SessionID) == "" {
			return errors.New("prior provider session id is required")
		}
		if request.PriorBinding.Generation == 0 {
			return errors.New("prior provider generation must be greater than zero")
		}
	default:
		return fmt.Errorf("unsupported session start mode %q", request.Mode)
	}
	return nil
}

// SessionAttacher opts a provider into recovery of the same live terminal.
// Attach must never create/resume a CLI. It returns the same provider session
// with a higher observer generation, or an error without a Start fallback.
// Detach discards only the exact observer, never the underlying terminal.
type SessionAttacher interface {
	SupportsAttach(domain.Provider) bool
	Attach(context.Context, StartSessionRequest) (domain.ProviderBinding, error)
	Detach(context.Context, StartSessionRequest, domain.ProviderBinding) error
}
