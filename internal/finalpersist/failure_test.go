package finalpersist_test

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"

	"bria/internal/cardhistory"
	"bria/internal/finalpersist"
)

func TestFinalFailureCopyNeverIncludesRawErrorOrPath(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{syscall.ENOSPC, "Недостаточно места"},
		{fs.ErrPermission, "Нет доступа"},
		{cardhistory.ErrFinalCapacity, "лимит сохраняемой истории"},
		{cardhistory.ErrFinalConflict, "перезапись запрещена"},
		{cardhistory.ErrFinalAnchor, "Не найдена запись"},
		{cardhistory.ErrFinalInvalid, "формату или лимиту"},
		{errors.New("SECRET_ERROR_SENTINEL"), "Ошибка чтения или записи"},
	} {
		text := finalpersist.FailureText(&os.PathError{Op: "write", Path: "SECRET_PATH_SENTINEL", Err: tc.err})
		if !strings.Contains(text, tc.want) || strings.Contains(text, "SECRET") {
			t.Fatalf("unsafe/inexact error copy: %q", text)
		}
	}
}
