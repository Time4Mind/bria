// Package cardhistory projects and restores typed histories without I/O.
package cardhistory

import (
	"errors"
	"strings"
	"unicode/utf8"

	"bria/internal/cardtranscript"
	"bria/internal/telegramhistory"
	"bria/internal/telegramstate"
)

var (
	ErrFinalInvalid  = errors.New("invalid recovered final")
	ErrFinalConflict = errors.New("recovered final conflicts with retained result")
	ErrFinalCapacity = errors.New("recovered final history capacity exceeded")
	ErrFinalAnchor   = errors.New("recovered final prompt anchor unavailable")
)

func RestoreFinal(card *telegramstate.Card, messageID, final string) error {
	if messageID == "" || final == "" || !utf8.ValidString(final) || len(final) > 32<<10 {
		return ErrFinalInvalid
	}
	var existing strings.Builder
	for i, key := range card.HistoryTurnKeys {
		if key == messageID && i < len(card.HistoryKinds) && card.HistoryKinds[i] == "final" {
			existing.WriteString(card.History[i])
		}
	}
	if existing.Len() > 0 {
		if existing.String() == final {
			return nil
		}
		return ErrFinalConflict
	}
	for text := final; text != ""; {
		size := min(len(text), 16<<10)
		for !utf8.ValidString(text[:size]) {
			size--
		}
		if len(card.History) >= 512 {
			return ErrFinalCapacity
		}
		if err := telegramhistory.InsertAfterPrompt(card, messageID, text[:size], "final"); err != nil {
			return ErrFinalAnchor
		}
		text = text[size:]
	}
	return nil
}

// Blocks returns projection values without editing the persisted source.
func Blocks(card telegramstate.Card, showTechnical bool) []cardtranscript.Block {
	blocks := make([]cardtranscript.Block, 0, len(card.History))
	for index, text := range card.History {
		kind := ""
		if len(card.HistoryKinds) > index {
			kind = card.HistoryKinds[index]
		}
		if kind == "" && len(card.HistoryKeys) > index && card.HistoryKeys[index] != "" {
			kind = "prompt"
		}
		if kind == "tool" && !showTechnical {
			continue
		}
		blocks = append(blocks, cardtranscript.Block{Kind: kind, Text: text})
	}
	return blocks
}
