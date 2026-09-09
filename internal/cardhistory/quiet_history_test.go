package cardhistory_test

import (
	"bria/internal/cardhistory"
	"bria/internal/telegramstate"
	"testing"
)

func TestObsoleteObservationNoticeIsHiddenButUserAndModelTextRemain(t *testing.T) {
	const notice = "Связь с CLI прервалась. Исход запроса пока не подтверждён."
	card := telegramstate.Card{History: []string{notice, notice, notice, "Ошибка CLI: запрос не выполнен."}, HistoryKinds: []string{"", "prompt", "final", ""}}
	for _, technical := range []bool{true, false} {
		blocks := cardhistory.Blocks(card, technical)
		if len(blocks) != 3 || blocks[0].Kind != "prompt" || blocks[1].Kind != "final" || blocks[2].Text != "Ошибка CLI: запрос не выполнен." {
			t.Fatalf("projection=%+v", blocks)
		}
		if len(card.History) != 4 {
			t.Fatal("source history mutated")
		}
	}
}
