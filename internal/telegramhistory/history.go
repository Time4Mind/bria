// Package telegramhistory manipulates validated card histories without persistence
// or lifecycle decisions. Callers own the card and its backing slices and retain
// responsibility for locking, validation, and atomic commit.
package telegramhistory

import (
	"fmt"

	"bria/internal/telegramstate"
)

// Append retains the latest 512 entries and aligns optional history metadata.
func Append(card *telegramstate.Card, item, kind string) {
	if len(card.History) >= 512 {
		card.History = append([]string(nil), card.History[len(card.History)-511:]...)
		if len(card.HistoryKeys) != 0 {
			card.HistoryKeys = append([]string(nil), card.HistoryKeys[len(card.HistoryKeys)-511:]...)
		}
		if len(card.HistoryKinds) != 0 {
			card.HistoryKinds = append([]string(nil), card.HistoryKinds[len(card.HistoryKinds)-511:]...)
		}
		if len(card.HistoryTurnKeys) != 0 {
			card.HistoryTurnKeys = append([]string(nil), card.HistoryTurnKeys[len(card.HistoryTurnKeys)-511:]...)
		}
	}
	if kind != "" && len(card.HistoryKinds) == 0 {
		card.HistoryKinds = make([]string, len(card.History))
	}
	card.History = append(card.History, item)
	if len(card.HistoryKeys) != 0 {
		card.HistoryKeys = append(card.HistoryKeys, "")
	}
	if len(card.HistoryKinds) != 0 {
		card.HistoryKinds = append(card.HistoryKinds, kind)
	}
	if len(card.HistoryTurnKeys) != 0 {
		card.HistoryTurnKeys = append(card.HistoryTurnKeys, "")
	}
}

// InsertAfterPrompt places an event after the last event for its originating
// prompt, ahead of later queued prompts. A missing anchor leaves history intact.
func InsertAfterPrompt(card *telegramstate.Card, promptID, item, kind string) error {
	index := -1
	for i, key := range card.HistoryTurnKeys {
		if key == promptID {
			index = i
		}
	}
	// The first event has no turn-key anchor yet; subsequent events use the
	// last event of the same turn to preserve provider event order.
	if index >= 0 {
		// continue with the latest same-turn index
	} else {
		for i, key := range card.HistoryKeys {
			if key == promptID {
				index = i
			}
		}
	}
	if index < 0 {
		return fmt.Errorf("prompt history anchor %q not found", promptID)
	}
	insert := index + 1
	card.History = append(card.History, "")
	copy(card.History[insert+1:], card.History[insert:])
	card.History[insert] = item
	if len(card.HistoryKeys) != 0 {
		card.HistoryKeys = append(card.HistoryKeys, "")
		copy(card.HistoryKeys[insert+1:], card.HistoryKeys[insert:])
		card.HistoryKeys[insert] = ""
	}
	if len(card.HistoryKinds) != 0 || kind != "" {
		if len(card.HistoryKinds) == 0 {
			card.HistoryKinds = make([]string, len(card.History)-1)
		}
		card.HistoryKinds = append(card.HistoryKinds, "")
		copy(card.HistoryKinds[insert+1:], card.HistoryKinds[insert:])
		card.HistoryKinds[insert] = kind
	}
	if len(card.HistoryTurnKeys) != 0 || promptID != "" {
		if len(card.HistoryTurnKeys) == 0 {
			card.HistoryTurnKeys = make([]string, len(card.History)-1)
		}
		card.HistoryTurnKeys = append(card.HistoryTurnKeys, "")
		copy(card.HistoryTurnKeys[insert+1:], card.HistoryTurnKeys[insert:])
		card.HistoryTurnKeys[insert] = promptID
	}
	return nil
}
