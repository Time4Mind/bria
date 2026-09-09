package main

import (
	"strings"
	"testing"

	"bria/internal/telegramcontroller"
	"bria/internal/telegramui"
)

func TestRestoredSplitFinalOpensWholeAnswerStartOnRichWire(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		f := newNavigationFollowFixture(t)
		f.click(telegramui.ActionPageLatest)
		if pinned {
			f.click(telegramui.ActionPagePrevious)
		}
		if err := f.store.SetCardPrompt(f.ctx, f.id, "restored-request", "question"); err != nil {
			t.Fatal(err)
		}
		answer := "RESTORED_ANSWER_BEGIN\n" + strings.Repeat("x", 20<<10) + "\nRESTORED_ANSWER_END"
		if err := f.store.RestoreAcceptedFinal(f.ctx, f.id, "restored-request", answer); err != nil {
			t.Fatal(err)
		}
		old, count := f.wire.last(), len(f.wire.snapshot())
		f.deliver(telegramcontroller.NotificationFinal, "restored-request:final")
		packets := f.wire.snapshot()
		if len(packets) != count+1 || packets[count].ID == old.ID || packets[count].Method != "sendRichMessage" ||
			!strings.Contains(packets[count].Text, "RESTORED_ANSWER_BEGIN") || strings.Contains(packets[count].Text, "RESTORED_ANSWER_END") {
			t.Fatal("restored final did not send one distinct card opening the complete answer's start")
		}
		state, err := f.store.LoadTelegramUI(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var restored strings.Builder
		for i, kind := range state.Cards[f.id].HistoryKinds {
			if kind == "final" && state.Cards[f.id].HistoryTurnKeys[i] == "restored-request" {
				restored.WriteString(state.Cards[f.id].History[i])
			}
		}
		if restored.String() != answer {
			t.Fatal("card rendering rewrote the restored answer")
		}
	}
}

func TestPendingRefreshCannotPublishFinalOnPreviousCard(t *testing.T) {
	for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationCommentary} {
		f := newNavigationFollowFixture(t)
		f.click(telegramui.ActionPageLatest)
		old := f.wire.last()
		if err := f.store.SetCardPrompt(f.ctx, f.id, "pending-request", "question"); err != nil {
			t.Fatal(err)
		}
		if err := f.store.RestoreAcceptedFinal(f.ctx, f.id, "pending-request", "FINAL_MUST_START_IN_NEW_MESSAGE"); err != nil {
			t.Fatal(err)
		}
		// A queued refresh may run after final history persistence but before
		// the final notification. It must not publish the final on the old card.
		f.deliver(kind, "late-refresh-before-final")
		for _, packet := range f.wire.snapshot() {
			if packet.ID == old.ID && strings.Contains(packet.Text, "FINAL_MUST_START_IN_NEW_MESSAGE") {
				t.Fatalf("%s published final by replacing previous card", kind)
			}
		}
	}
}

func TestQueuedFinalCardsPublishTheirOwnAnswerAndClearOnlyTheirFence(t *testing.T) {
	f := newNavigationFollowFixture(t)
	f.click(telegramui.ActionPageLatest)
	for _, request := range []string{"queued-A", "queued-B"} {
		if err := f.store.SetCardPrompt(f.ctx, f.id, request, "question"); err != nil {
			t.Fatal(err)
		}
		if err := f.store.RestoreAcceptedFinal(f.ctx, f.id, request, request+"_BEGIN\n"+strings.Repeat("answer continuation\n", 240)+request+"_END"); err != nil {
			t.Fatal(err)
		}
	}
	for index, request := range []string{"queued-A", "queued-B"} {
		old, count := f.wire.last(), len(f.wire.snapshot())
		f.deliver(telegramcontroller.NotificationFinal, request+":final")
		packets := f.wire.snapshot()
		if len(packets) != count+1 || packets[count].ID == old.ID || packets[count].Method != "sendRichMessage" ||
			!strings.Contains(packets[count].Text, request+"_BEGIN") || strings.Contains(packets[count].Text, request+"_END") {
			t.Fatal("queued final did not publish its own beginning on a new carrier")
		}
		state, err := f.store.LoadTelegramUI(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		pending := state.Cards[f.id].PendingFinalOperations
		if index == 0 && (len(pending) != 1 || pending[0] != "queued-B:final") || index == 1 && len(pending) != 0 {
			t.Fatal("final receipt cleared another final's fence or left its own armed")
		}
	}
}
