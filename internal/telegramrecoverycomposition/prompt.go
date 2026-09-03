package telegramrecoverycomposition

import (
	"errors"
	"fmt"

	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
	"bria/internal/telegramui"
)

func PrepareStatusRecovery(operationID string, conversationID int64, binding telegramflow.StatusRecoveryBinding, presenter *telegrambridge.Presenter) (telegramflow.Prepared, error) {
	if operationID != binding.OperationID || conversationID != binding.Carrier.ChatID {
		return telegramflow.Prepared{}, errors.New("status recovery operation and carrier are invalid")
	}
	text := fmt.Sprintf("Неизвестно, доставлено ли сообщение операции %s. Повтор может создать дубль.", binding.OperationID)
	keyboard := telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{
		{{Action: telegramui.ActionStatusRecoveryAssumeDelivered}},
		{{Action: telegramui.ActionStatusRecoveryRetryPossibleDuplicate}},
		{{Action: telegramui.ActionStatusRecoveryCancel}},
	}}
	copyBinding := binding
	return telegramflow.PrepareSurface(operationID, conversationID, "", binding.Carrier.MessageID, binding.Edit,
		telegramflow.SurfaceOutput{Text: text, Keyboard: keyboard, StatusRecovery: &copyBinding}, presenter)
}
