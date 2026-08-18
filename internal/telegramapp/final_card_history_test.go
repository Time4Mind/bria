package telegramapp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

func TestFinalFreezesHistoricalCardAndPostsLatestResponseOnce(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session := fixture.machine.State().Sessions[ref.Key()]
	promptAt := time.Now().Add(-5 * time.Second).UTC()
	applyBackgroundCommand(t, fixture, "history-final-prompt",
		clusterstate.CommandPublishSessionRuntime, promptAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: session.RuntimeGeneration,
			Phase: domain.RuntimeRunning, Result: &domain.SessionOperationResult{
				OperationID: "history-final-input", Action: domain.ActionSendInput,
				Status: domain.OperationQueued,
			},
		})
	running := fixture.machine.State().Sessions[ref.Key()]
	finalAt := promptAt.Add(3 * time.Second)
	applyBackgroundCommand(t, fixture, "history-final-settled",
		clusterstate.CommandPublishSessionRuntime, finalAt.Add(500*time.Millisecond),
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: running.RuntimeGeneration, Phase: domain.RuntimeIdle,
		})
	settled := fixture.machine.State().Sessions[ref.Key()]
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "history-final-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 82, Rich: true, Session: ref,
			SessionRevision: settled.Revision, SessionEventAt: settled.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{
		{Kind: transcript.EventAssistantFinal, Text: "Older answer one", Timestamp: promptAt.Add(-4 * time.Second).Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal, Text: "Older answer two", Timestamp: promptAt.Add(-3 * time.Second).Format(time.RFC3339Nano)},
		{Kind: transcript.EventUserText, Text: "current prompt", Timestamp: promptAt.Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal,
			Text:      "LATEST RESPONSE START " + strings.Repeat("middle ", 1200) + "LATEST RESPONSE END",
			Timestamp: finalAt.Format(time.RFC3339Nano)},
	}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	pageToken, err := fixture.codec.Page(7, telegramui.ActionPagePrevious, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	callbackData, err := (telegramui.Callback{
		Action: telegramui.ActionPagePrevious, Token: pageToken,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 501, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "read-history", CallbackData: callbackData,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 82, Rich: true},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.sent) != 0 || len(fixture.messenger.edited) != 1 {
		t.Fatalf("historical navigation sent=%d edited=%d", len(fixture.messenger.sent), len(fixture.messenger.edited))
	}
	historicalLabel := fixture.messenger.edited[0].Grid[0][1].Label
	if !strings.HasPrefix(historicalLabel, "1/") {
		t.Fatalf("historical page = %s", historicalLabel)
	}

	// Simulate the daemon restart that used to lose the pinned page, then let
	// final reconciliation promote the completed response.
	handler, err = telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	runBackgroundForTest(handler, 60*time.Millisecond)
	if len(fixture.messenger.sent) != 1 {
		t.Fatalf("final carriers = %d", len(fixture.messenger.sent))
	}
	finalScreen := fixture.messenger.sent[0]
	if !strings.Contains(finalScreen.Text, "LATEST RESPONSE START") ||
		strings.Contains(finalScreen.Text, "LATEST RESPONSE END") {
		t.Fatalf("new carrier did not start at latest response: %q", finalScreen.Text)
	}
	if fixture.messenger.edited[0].Grid[0][1].Label != historicalLabel {
		t.Fatalf("historical card changed after final: %#v", fixture.messenger.edited[0])
	}
	for index := 1; index < len(fixture.messenger.editedMessages); index++ {
		if fixture.messenger.editedMessages[index].MessageID == 82 {
			t.Fatalf("historical card was edited after final: %#v", fixture.messenger.editedMessages)
		}
	}
	if containsMessageID(fixture.messenger.deleted, 82) {
		t.Fatalf("historical card was deleted: %#v", fixture.messenger.deleted)
	}
	if !containsMessageID(fixture.messenger.cleared, 82) {
		t.Fatalf("historical keyboard was not cleared: %#v", fixture.messenger.cleared)
	}

	runBackgroundForTest(handler, 40*time.Millisecond)
	if len(fixture.messenger.sent) != 1 {
		t.Fatalf("final reconciliation duplicated carrier: %d", len(fixture.messenger.sent))
	}
}

func runBackgroundForTest(handler *telegramapp.Handler, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
}

func containsMessageID(messages []telegrambot.Message, messageID int64) bool {
	for _, message := range messages {
		if message.MessageID == messageID {
			return true
		}
	}
	return false
}
