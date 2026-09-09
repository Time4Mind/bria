package turnfinalization

import "bria/internal/sessionruntime"

// FailureText preserves the existing terminal/observation distinction.
func FailureText(result sessionruntime.TurnResult, unknown, terminalFailure bool) string {
	text := "Ошибка CLI: запрос не выполнен."
	if unknown {
		text = "Связь с CLI прервалась. Исход запроса пока не подтверждён."
	}
	if result.ErrorCode == sessionruntime.ErrorAuthenticationFailed {
		text = "Ошибка авторизации Claude: требуется выполнить вход (/login)."
	}
	if terminalFailure && result.TerminalStatus == sessionruntime.StatusInterrupted {
		text = "Запрос остановлен."
	}
	return text
}
