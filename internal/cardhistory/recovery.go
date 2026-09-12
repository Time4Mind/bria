// Package cardhistory projects and restores typed histories without I/O.
package cardhistory

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"bria/internal/cardtranscript"
	"bria/internal/telegramhistory"
	"bria/internal/telegramhistorylimit"
	"bria/internal/telegramstate"
)

var (
	ErrFinalInvalid  = errors.New("invalid recovered final")
	ErrFinalConflict = errors.New("recovered final conflicts with retained result")
	ErrFinalCapacity = errors.New("recovered final history capacity exceeded")
	ErrFinalAnchor   = errors.New("recovered final prompt anchor unavailable")
)

func RestoreFinal(card *telegramstate.Card, messageID, final string) error {
	if card == nil || messageID == "" || strings.TrimSpace(messageID) != messageID || len(messageID) > 1024 || !utf8.ValidString(messageID) || final == "" || !utf8.ValidString(final) || len(final) > 32<<10 {
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
	operation := messageID + ":final"
	alreadyPending := slices.Contains(card.PendingFinalOperations, operation)
	if !alreadyPending && len(card.PendingFinalOperations) >= 512 {
		return ErrFinalCapacity
	}
	parts := make([]string, 0, 2)
	for text := final; text != ""; {
		size := min(len(text), 16<<10)
		for !utf8.ValidString(text[:size]) {
			size--
		}
		parts = append(parts, text[:size])
		text = text[size:]
	}
	if err := telegramhistorylimit.EnsureRoomAfterPrompt(card, messageID, len(parts)); err != nil {
		if errors.Is(err, telegramhistory.ErrPromptAnchorUnavailable) {
			return ErrFinalAnchor
		}
		return ErrFinalCapacity
	}
	for _, part := range parts {
		if err := telegramhistory.InsertAfterPrompt(card, messageID, part, "final"); err != nil {
			return ErrFinalAnchor
		}
	}
	if !alreadyPending {
		card.PendingFinalOperations = append(card.PendingFinalsAfter(""), operation)
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
		// Old releases stored observation loss as an untyped synthetic history
		// entry. Hide that notice without deleting history or filtering user/model text.
		if kind == "" && text == "Связь с CLI прервалась. Исход запроса пока не подтверждён." {
			continue
		}
		if kind == "tool" && !showTechnical {
			continue
		}
		continuation := kind == "final" && index > 0 && len(card.HistoryTurnKeys) > index &&
			card.HistoryTurnKeys[index] != "" && card.HistoryTurnKeys[index] == card.HistoryTurnKeys[index-1] &&
			len(card.HistoryKinds) > index-1 && card.HistoryKinds[index-1] == "final" && strings.TrimSpace(card.History[index-1]) != ""
		operation := ""
		if kind == "final" && len(card.HistoryTurnKeys) > index && card.HistoryTurnKeys[index] != "" {
			operation = card.HistoryTurnKeys[index] + ":final"
		}
		blocks = append(blocks, cardtranscript.Block{Kind: kind, Text: text, FinalContinuation: continuation, FinalOperationID: operation})
	}
	return blocks
}
