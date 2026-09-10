package sessionruntime

import (
	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/runtimeprotocol"
	"context"
	"errors"
)

var _ app.SessionAttacher = (*Starter)(nil)

func (starter *Starter) SupportsAttach(provider domain.Provider) bool {
	if starter == nil {
		return false
	}
	command, ok := starter.commands[provider]
	return ok && command.Spec.PersistentTerminal
}

// Attach starts only an observer of an exact persisted terminal, never a CLI.
func (starter *Starter) Attach(ctx context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	if !starter.SupportsAttach(request.Provider) || request.Mode != app.SessionStartResume || request.PriorBinding == nil {
		return domain.ProviderBinding{}, ErrBindingMismatch
	}
	binding, err := starter.start(ctx, request, true)
	if err != nil && StartupFailureClass(err) == "terminal_unavailable" {
		err = errors.Join(app.ErrTerminalUnavailable, err)
	}
	return binding, err
}

func (starter *Starter) Detach(ctx context.Context, request app.StartSessionRequest, binding domain.ProviderBinding) error {
	if !starter.SupportsAttach(request.Provider) {
		return ErrBindingMismatch
	}
	return starter.release(ctx, request, binding, "detach")
}

// ObserveAcceptedWithCallbacks is deliberately incapable of carrying new input.
func (starter *Starter) ObserveAcceptedWithCallbacks(ctx context.Context, sessionID domain.SessionID, binding domain.ProviderBinding, callbacks TurnCallbacks) (TurnResult, error) {
	if _, err := runtimeprotocol.EncodeParentLine(runtimeprotocol.ParentMessage{Protocol: ProtocolVersion, Type: runtimeprotocol.TypeObserveAccepted, RequestID: "observe", MessageID: callbacks.MessageID}, runtimeprotocol.Limits{}); err != nil {
		return TurnResult{}, err
	}
	return starter.consumeTurn(ctx, sessionID, StructuredInput{}, callbacks, &binding)
}
