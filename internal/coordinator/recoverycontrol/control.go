// Package recoverycontrol defines the transport-neutral durable identity fence
// between one unknown operation and its separate signed recovery prompt.
package recoverycontrol

import (
	"errors"
	"strings"
)

type Control struct {
	OriginalOperationID string
	PromptOperationID   string
	UpdateID            int64
}

func Validate(control Control, updateID int64) error {
	if control.UpdateID != updateID || control.UpdateID <= 0 || strings.TrimSpace(control.OriginalOperationID) == "" || strings.TrimSpace(control.PromptOperationID) == "" || control.OriginalOperationID == control.PromptOperationID {
		return errors.New("recovery control must bind distinct exact operations to its update")
	}
	return nil
}
