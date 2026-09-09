// Package telegramsessionview formats committed lifecycle state without I/O.
package telegramsessionview

import "bria/internal/domain"

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
