package turnfinalization

import (
	"bria/internal/app"
	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/finalpersist"
	"bria/internal/sessioncloseflow"
	"bria/internal/telegramcontrolport"
	"context"
	"errors"
)

type Lifecycle struct {
	Turns      telegramcontrolport.TurnLifecycle
	Closer     telegramcontrolport.SessionCloser
	CloseFlow  *sessioncloseflow.Flow
	Replace    func(domain.Session)
	Apply      func(context.Context, domain.Session) error
	Notify     func(string, string)
	finished   *domain.Session
	closeAfter bool
	closed     *app.CloseSessionResult
}

func (l *Lifecycle) Finish(ctx context.Context, id domain.SessionID, messageID string) (bool, error) {
	if l.Turns == nil {
		return true, nil
	}
	finishContext := context.WithoutCancel(ctx)
	if l.finished == nil {
		finished, closeAfter, err := l.Turns.Finish(finishContext, id)
		if err != nil {
			l.Notify(messageID+":lifecycle-finish", "Не удалось сохранить завершение запроса.")
			return false, err
		}
		l.finished, l.closeAfter = &finished, closeAfter
		l.Replace(finished)
	}
	if !l.closeAfter {
		return l.finished.Status() == domain.SessionReady, nil
	}
	finishContext = l.CloseFlow.CompletionContext(finishContext, id)
	if l.Closer == nil {
		l.Notify(messageID+":close-missing", "Сессия ожидает закрытия, но обработчик закрытия не настроен.")
		return false, errors.New("session closer is unavailable")
	}
	if l.closed == nil {
		closed, err := l.Closer.Close(finishContext, id)
		l.CloseFlow.Outcome(finishContext, id, closed, err, controllertelemetry.ScheduledClose)
		if err != nil {
			l.Notify(messageID+":close-error", "Не удалось подтвердить закрытие сессии.")
			return false, err
		}
		l.closed = &closed
	}
	if err := l.Apply(finishContext, l.closed.Session); err != nil {
		l.Notify(messageID+":close-ui-state", "Не удалось сохранить закрытие сессии.")
		return false, err
	}
	return l.closed.Deleted || l.closed.Session.Status() == domain.SessionArchived, nil
}

func (l Lifecycle) Retry(ctx context.Context, id domain.SessionID, binding domain.ProviderBinding, message string, sessions finalpersist.Sessions, failed func(uint64, error)) (bool, error) {
	attempted, valid := false, false
	err := Retry(ctx, id, binding, sessions, func(attempt context.Context) error {
		if attempted && l.finished == nil {
			current, err := sessions.Load(attempt, id)
			if err != nil {
				return err
			}
			if current.Status() == domain.SessionReady || current.Status() == domain.SessionArchived {
				l.Replace(current)
				valid = true
				return nil
			}
		}
		attempted = true
		var err error
		valid, err = l.Finish(attempt, id, message)
		return err
	}, failed)
	return valid, err
}
