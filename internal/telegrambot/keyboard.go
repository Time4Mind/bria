package telegrambot

import (
	"fmt"

	"github.com/Time4Mind/bria/internal/telegramui"
)

const (
	MaxKeyboardRows     = 100
	MaxButtonsPerRow    = 8
	MaxButtonLabelBytes = 128
)

func convertGrid(grid telegramui.Grid) (inlineKeyboardMarkup, error) {
	if len(grid) > MaxKeyboardRows {
		return inlineKeyboardMarkup{}, fmt.Errorf("keyboard exceeds %d rows", MaxKeyboardRows)
	}
	rows := make([][]inlineKeyboardButton, 0, len(grid))
	for rowIndex, row := range grid {
		if len(row) == 0 || len(row) > MaxButtonsPerRow {
			return inlineKeyboardMarkup{}, fmt.Errorf("keyboard row %d has invalid size", rowIndex)
		}
		buttons := make([]inlineKeyboardButton, 0, len(row))
		for buttonIndex, button := range row {
			if len([]byte(button.Label)) == 0 || len([]byte(button.Label)) > MaxButtonLabelBytes {
				return inlineKeyboardMarkup{}, fmt.Errorf(
					"keyboard button %d:%d has invalid label size", rowIndex, buttonIndex,
				)
			}
			callbackData, err := button.Callback.Encode()
			if err != nil {
				return inlineKeyboardMarkup{}, fmt.Errorf(
					"keyboard button %d:%d: %w", rowIndex, buttonIndex, err,
				)
			}
			buttons = append(buttons, inlineKeyboardButton{
				Text: button.Label, CallbackData: callbackData,
			})
		}
		rows = append(rows, buttons)
	}
	return inlineKeyboardMarkup{InlineKeyboard: rows}, nil
}
