package turnfinalization_test

import (
	"testing"

	"bria/internal/sessionruntime"
	"bria/internal/turnfinalization"
)

func TestFailureTextObservationLossAndProvenErrors(t *testing.T) {
	for _, tc := range []struct {
		name              string
		result            sessionruntime.TurnResult
		unknown, terminal bool
		want              string
	}{
		{name: "observation loss", unknown: true},
		{name: "authentication despite observation loss", unknown: true, result: sessionruntime.TurnResult{ErrorCode: sessionruntime.ErrorAuthenticationFailed}, want: "Ошибка авторизации Claude: требуется выполнить вход (/login)."},
		{name: "proven failure", terminal: true, result: sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusFailed}, want: "Ошибка CLI: запрос не выполнен."},
		{name: "interrupted", terminal: true, result: sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusInterrupted}, want: "Запрос остановлен."},
		{name: "unaccepted failure", want: "Ошибка CLI: запрос не выполнен."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := turnfinalization.FailureText(tc.result, tc.unknown, tc.terminal); got != tc.want {
				t.Fatalf("FailureText = %q, want %q", got, tc.want)
			}
		})
	}
}
