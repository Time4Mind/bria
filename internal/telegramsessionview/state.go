// Package telegramsessionview formats committed lifecycle state without I/O.
package telegramsessionview

import (
	"fmt"
	"time"

	"bria/internal/domain"
)

var moscowTime = time.FixedZone("MSK", 3*60*60)

func Header(label, nodeName string, provider domain.Provider, stateText string, lastEventUnixNano int64) string {
	header := fmt.Sprintf("%s · %s · %s · %s", label, nodeName, provider, stateText)
	if lastEventUnixNano > 0 {
		header += " · " + time.Unix(0, lastEventUnixNano).In(moscowTime).Format("15:04:05")
	}
	return header + "\n\n─────  \n"
}

// RecoveryNotice leaves automatic recovery to the lifecycle worker. The state
// badge remains truthful; the card must not demand an owner's repair action.
func RecoveryNotice(session domain.Session, busy, available bool) string {
	if session.Status() != domain.SessionAwaitingRecovery || available || busy {
		return ""
	}
	text := "Требуется восстановление. История сохранена; ввод отключён.  \n"
	if target, ok := session.RecoveryTarget(); ok && (target == domain.SessionRunning || target == domain.SessionStopping || target == domain.SessionClosingAfterWork) {
		text += "Исход предыдущего запроса не подтверждён; повторная отправка не выполняется.  \n"
	}
	if busy {
		text += "Проверяю сохранённый исход запроса.  \n"
	}
	if !available {
		text += "Восстановление сейчас недоступно.  \n"
	}
	return text
}

func StateText(status domain.SessionStatus) string {
	switch status {
	case domain.SessionStarting:
		return "запускается"
	case domain.SessionResuming:
		return "возобновляется"
	case domain.SessionReady:
		return "готова"
	case domain.SessionRunning:
		return "в работе"
	case domain.SessionStopping:
		return "останавливается"
	case domain.SessionClosingAfterWork, domain.SessionClosing:
		return "архивируется"
	case domain.SessionAwaitingRecovery:
		return "ожидает восстановления"
	case domain.SessionResumeFailed:
		return "ошибка"
	default:
		return string(status)
	}
}

func StatusGlyph(status domain.SessionStatus) string {
	switch status {
	case domain.SessionRunning, domain.SessionStarting, domain.SessionResuming, domain.SessionStopping, domain.SessionClosingAfterWork, domain.SessionClosing:
		return "⏳"
	case domain.SessionResumeFailed:
		return "❌"
	case domain.SessionAwaitingRecovery:
		return "❓"
	default:
		return "✅"
	}
}
