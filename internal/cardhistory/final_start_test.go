package cardhistory_test

import (
	"reflect"
	"strings"
	"testing"

	"bria/internal/cardhistory"
	"bria/internal/cardtranscript"
	"bria/internal/telegramstate"
)

func TestRestoredFinalChunksKeepOneActualBeginning(t *testing.T) {
	card := telegramstate.Card{History: []string{"question"}, HistoryKeys: []string{"request"}, HistoryKinds: []string{"prompt"}}
	answer := "ANSWER START\n" + strings.Repeat("x", 20<<10) + "\nANSWER END"
	if err := cardhistory.RestoreFinal(&card, "request", answer); err != nil {
		t.Fatal(err)
	}
	before := card
	pages := cardtranscript.Paginate(cardtranscript.RenderBlocks(cardhistory.Blocks(card, true)), 32)
	starts := 0
	for _, page := range pages {
		if page.FinalStart {
			starts++
			if !strings.HasPrefix(page.Content, "ANSWER START") {
				t.Fatal("continuation was advertised as the beginning of the answer")
			}
		}
	}
	if starts != 1 || !reflect.DeepEqual(card, before) {
		t.Fatalf("starts=%d; projection must keep one actual start without changing history", starts)
	}
}

func TestDistinctAndUnprovenFinalChunksAreNotConflated(t *testing.T) {
	for _, keys := range [][]string{{"first", "second"}, {"", ""}, {"first", ""}} {
		card := telegramstate.Card{History: []string{"first answer", "second answer"}, HistoryKinds: []string{"final", "final"}, HistoryTurnKeys: keys}
		pages := cardtranscript.Paginate(cardtranscript.RenderBlocks(cardhistory.Blocks(card, true)), 32)
		if len(pages) != 2 || !pages[0].FinalStart || !pages[1].FinalStart {
			t.Fatalf("distinct or unproven answers were conflated: keys=%v", keys)
		}
	}
}
