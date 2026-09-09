package turnfinalization

import "bria/internal/sessionruntime"

// FailureText returns no user-facing text for observation loss alone.
// Callers must skip history and notification writes when the text is empty.
func FailureText(result sessionruntime.TurnResult, unknown, terminalFailure bool) string {
	text := "Ошибка CLI: запрос не выполнен."
	if unknown {
		text = ""
	}
	if result.ErrorCode == sessionruntime.ErrorAuthenticationFailed {
		text = "Ошибка авторизации Claude: требуется выполнить вход (/login)."
	}
	if terminalFailure && result.TerminalStatus == sessionruntime.StatusInterrupted {
		text = "Запрос остановлен."
	}
	return text
}
