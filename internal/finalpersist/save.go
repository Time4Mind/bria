package finalpersist

import (
	"context"
	"errors"

	"bria/internal/domain"
)

type Request struct {
	SessionID       domain.SessionID
	Binding         domain.ProviderBinding
	MessageID, Text string
	RequireRunning  bool
}

type Sessions interface {
	Load(context.Context, domain.SessionID) (domain.Session, error)
}
type Writer interface {
	RestoreAcceptedFinal(context.Context, domain.SessionID, string, string) error
}
type boundWriter interface {
	RestoreAcceptedFinalForSession(context.Context, domain.Session, string, string) (bool, error)
}

// Save retries one immutable final, never a provider request or lifecycle action.
func Save(ctx context.Context, request Request, sessions Sessions, writer Writer, onFailure func(uint64, error)) error {
	return Retry(ctx, DefaultPolicy(), func(attemptCtx context.Context) error {
		current, err := sessions.Load(attemptCtx, request.SessionID)
		if err != nil {
			return err
		}
		binding, bound := current.Binding()
		active := current.Status() == domain.SessionRunning || current.Status() == domain.SessionStopping || current.Status() == domain.SessionClosingAfterWork
		if current.ID() != request.SessionID || !bound || binding != request.Binding || !active && (request.RequireRunning || current.Status() != domain.SessionReady) {
			return ErrSuperseded
		}
		if atomic, ok := writer.(boundWriter); ok {
			saved, err := atomic.RestoreAcceptedFinalForSession(attemptCtx, current, request.MessageID, request.Text)
			if err != nil {
				return err
			}
			if !saved {
				return errors.New("final persistence snapshot changed")
			}
			return nil
		}
		return writer.RestoreAcceptedFinal(attemptCtx, request.SessionID, request.MessageID, request.Text)
	}, onFailure)
}
