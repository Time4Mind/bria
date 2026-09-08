package finalpersist

import (
	"errors"
	"io/fs"
	"syscall"

	"bria/internal/cardhistory"
)

// FailureText returns closed safe copy, never paths or raw error payloads.
func FailureText(err error) string {
	switch {
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return "Недостаточно места для записи."
	case errors.Is(err, fs.ErrPermission):
		return "Нет доступа к записи состояния."
	case errors.Is(err, cardhistory.ErrFinalCapacity):
		return "Достигнут лимит сохраняемой истории."
	case errors.Is(err, cardhistory.ErrFinalConflict):
		return "Сохранённый финал отличается; перезапись запрещена."
	case errors.Is(err, cardhistory.ErrFinalAnchor):
		return "Не найдена запись исходного запроса в истории."
	case errors.Is(err, cardhistory.ErrFinalInvalid):
		return "Финал не соответствует формату или лимиту истории."
	default:
		return "Ошибка чтения или записи состояния."
	}
}
