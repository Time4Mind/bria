package turnfinalization

import (
	"bria/internal/controllertelemetry"
	"bria/internal/finalpersist"
	"bria/internal/telegramcontrolport"
	"context"
)

// Save retains the existing final-save notification and safe diagnostic policy.
func Save(ctx context.Context, request finalpersist.Request, sessions finalpersist.Sessions, writer finalpersist.Writer, chatID int64, observe func(context.Context, controllertelemetry.Event), notify func(context.Context, telegramcontrolport.Notification) bool) error {
	ctx = controllertelemetry.WithOperation(ctx, request.MessageID+":final-save")
	retrying := false
	event := controllertelemetry.Event{Stage: controllertelemetry.FinalSave, OperationID: request.MessageID + ":final-save", ParentOperationID: request.MessageID, SessionID: string(request.SessionID), Outcome: controllertelemetry.Failed, Reason: controllertelemetry.PersistFailed}
	err := finalpersist.Save(ctx, request, sessions, writer, func(attempt uint64, failure error) {
		retrying = true
		observe(context.WithoutCancel(ctx), event)
		if attempt == 1 {
			notify(ctx, telegramcontrolport.Notification{OperationID: request.MessageID + ":final-history-error", ConversationID: chatID, SessionID: request.SessionID, Kind: telegramcontrolport.NotificationError, Text: "Не удалось сохранить финальный ответ. " + finalpersist.FailureText(failure) + " Повторю запись автоматически; очередь пока ожидает. Запрос модели не повторяется."})
		}
	})
	if retrying && err == nil {
		event.Outcome, event.Reason = controllertelemetry.Persisted, controllertelemetry.ReasonUnknown
		observe(context.WithoutCancel(ctx), event)
		notify(ctx, telegramcontrolport.Notification{OperationID: request.MessageID + ":final-history-restored", ConversationID: chatID, SessionID: request.SessionID, Kind: telegramcontrolport.NotificationPromptStatus, Text: "Финальный ответ сохранён. Завершаю запрос."})
	}
	return err
}
