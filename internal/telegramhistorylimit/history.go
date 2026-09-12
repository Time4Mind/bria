// Package telegramhistorylimit reserves bounded card-history capacity while
// retaining prompt anchors and aligned metadata.
package telegramhistorylimit

import (
	"fmt"
	"slices"

	"bria/internal/telegramhistory"
	"bria/internal/telegramstate"
)

const maxEntries = 512

// InsertAfterPrompt reserves one entry before delegating the ordered insert.
func InsertAfterPrompt(card *telegramstate.Card, promptID, item, kind string) error {
	if err := EnsureRoomAfterPrompt(card, promptID, 1); err != nil {
		return err
	}
	return telegramhistory.InsertAfterPrompt(card, promptID, item, kind)
}

// EnsureRoomAfterPrompt reserves all requested entries before a multi-part
// insert begins, so a capacity failure cannot leave only part of a final.
func EnsureRoomAfterPrompt(card *telegramstate.Card, promptID string, entries int) error {
	if card == nil || len(card.History) == 0 {
		return fmt.Errorf("history is unavailable")
	}
	if entries < 1 || entries > maxEntries || len(card.History) > maxEntries {
		return fmt.Errorf("invalid history capacity request")
	}
	for _, metadata := range [][]string{card.HistoryKeys, card.HistoryKinds, card.HistoryTurnKeys} {
		if len(metadata) != 0 && len(metadata) != len(card.History) {
			return fmt.Errorf("history metadata is not aligned")
		}
	}
	anchor := anchorIndex(card, promptID)
	if anchor < 0 {
		return fmt.Errorf("%w: %q", telegramhistory.ErrPromptAnchorUnavailable, promptID)
	}
	need := len(card.History) + entries - maxEntries
	if need <= 0 {
		return nil
	}
	otherTurn, oldPrompts, currentTurn := make([]int, 0, need), make([]int, 0, need), make([]int, 0, need)
	for index := range card.History {
		key, kind, turn := valueAt(card.HistoryKeys, index), valueAt(card.HistoryKinds, index), valueAt(card.HistoryTurnKeys, index)
		if kind == "prompt" || key != "" && turn == "" {
			if index < anchor && key != promptID && !pendingFinal(card, key) {
				oldPrompts = append(oldPrompts, index)
			}
			continue
		}
		if kind == "final" && pendingFinal(card, turn) {
			continue
		}
		if key == promptID || turn == promptID {
			if index != anchor {
				currentTurn = append(currentTurn, index)
			}
		} else {
			otherTurn = append(otherTurn, index)
		}
	}
	candidates := append(append(otherTurn, oldPrompts...), currentTurn...)
	if len(candidates) < need {
		return fmt.Errorf("history has only %d evictable entries for %d required", len(candidates), need)
	}
	selected := append([]int(nil), candidates[:need]...)
	slices.Sort(selected)
	slices.Reverse(selected)
	for _, index := range selected {
		card.History = removeAt(card.History, index)
		if len(card.HistoryKeys) != 0 {
			card.HistoryKeys = removeAt(card.HistoryKeys, index)
		}
		if len(card.HistoryKinds) != 0 {
			card.HistoryKinds = removeAt(card.HistoryKinds, index)
		}
		if len(card.HistoryTurnKeys) != 0 {
			card.HistoryTurnKeys = removeAt(card.HistoryTurnKeys, index)
		}
	}
	return nil
}

func anchorIndex(card *telegramstate.Card, promptID string) int {
	for index, key := range card.HistoryKeys {
		if key == promptID {
			return index
		}
	}
	anchor := -1
	for index, turn := range card.HistoryTurnKeys {
		if turn == promptID {
			anchor = index
		}
	}
	return anchor
}

func pendingFinal(card *telegramstate.Card, turn string) bool {
	for _, operation := range card.PendingFinalOperations {
		if turn != "" && operation == turn+":final" {
			return true
		}
	}
	return false
}

func valueAt(values []string, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return values[index]
}

func removeAt(values []string, index int) []string {
	copy(values[index:], values[index+1:])
	values[len(values)-1] = ""
	return values[:len(values)-1]
}
