package telegramrecovery

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/telegramui"
)

const (
	EffectUnknownPhase      = "effect_unknown"
	EffectRetryUnknownPhase = "effect_retry_unknown"
	SendUnknownPhase        = "send_unknown"
)

type AcceptedTurnBinding struct {
	SessionID         domain.SessionID
	MessageID         string
	BindingGeneration uint64
}

func ValidAcceptedTurnBinding(binding *AcceptedTurnBinding) bool {
	return binding != nil && binding.SessionID != "" && binding.SessionID != domain.SessionID(telegramui.GlobalSurfaceID) &&
		strings.TrimSpace(binding.MessageID) != "" && len(binding.MessageID) <= 256 &&
		utf8.ValidString(binding.MessageID) && binding.BindingGeneration > 0
}

func CloneAcceptedTurnBinding(binding *AcceptedTurnBinding) *AcceptedTurnBinding {
	if binding == nil {
		return nil
	}
	clone := *binding
	return &clone
}

func ProjectUnknown(phase, sessionID, operationID string) (string, telegramui.CardKeyboard, error) {
	var message string
	var actions []telegramui.Action
	switch phase {
	case EffectUnknownPhase:
		message = "Неизвестно, было ли выполнено действие."
		actions = []telegramui.Action{
			telegramui.ActionCallbackEffectConfirmed,
			telegramui.ActionCallbackEffectRetryPossibleDuplicate,
		}
	case EffectRetryUnknownPhase:
		message = "Неизвестно, было ли выполнено повторное действие. Повтор уже запрещён."
		actions = []telegramui.Action{telegramui.ActionCallbackEffectConfirmed}
	case SendUnknownPhase:
		message = "Неизвестно, доставлено ли сообщение."
		actions = []telegramui.Action{
			telegramui.ActionCallbackSendConfirmed,
			telegramui.ActionCallbackSendRetryPossibleDuplicate,
		}
	default:
		return "", telegramui.CardKeyboard{}, errors.New("callback operation is not in an unknown phase")
	}

	text := fmt.Sprintf("%s Сессия: %s. Операция: %s. Повтор может создать дубль.", message, sessionID, operationID)
	row := make(telegramui.ButtonRow, len(actions))
	for index, action := range actions {
		row[index] = telegramui.Button{Action: action}
	}
	keyboard := telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{row}}
	return text, keyboard, nil
}

func ProjectAcceptedTurn() (string, telegramui.CardKeyboard) {
	text := "Неизвестно, выполнился ли ранее принятый запрос. Повтор может повторно выполнить внешние действия и создать дубль."
	keyboard := telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{
		{{Action: telegramui.ActionAcceptedTurnAssumeCompleted}},
		{{Action: telegramui.ActionAcceptedTurnRetryPossibleDuplicate}},
		{{Action: telegramui.ActionAcceptedTurnCancel}},
	}}
	return text, keyboard
}

func MultipleBindings(values ...bool) bool {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count > 1
}
