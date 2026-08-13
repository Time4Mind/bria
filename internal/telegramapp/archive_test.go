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

func TestArchiveInspectShowsLastPageAndSeparateHistory(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := addReadyArchive(t, fixture, actor)
	controls := &blockingControls{ref: ref, events: []transcript.Event{
		{Kind: transcript.EventAssistantFinal, Text: "first archived answer"},
		{Kind: transcript.EventAssistantFinal, Text: "last archived answer"},
	}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	inspectToken, err := fixture.codec.Archive(7, telegramui.ActionSelectArchive, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	invokeArchiveCallback(t, handler, 91, telegramui.ActionSelectArchive, inspectToken)
	inspect := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(inspect.Text, "last archived answer") ||
		strings.Contains(inspect.Text, "first archived answer") {
		t.Fatalf("inspect text=%q", inspect.Text)
	}
	grid := telegramui.CanonicalGrid(inspect.Grid)
	for _, action := range []string{"restore", "archive_history", "archive_back"} {
		if !strings.Contains(grid, action) {
			t.Fatalf("inspect grid=%s", grid)
		}
	}

	historyToken, err := fixture.codec.Page(7, telegramui.ActionArchiveHistory, ref, 2)
	if err != nil {
		t.Fatal(err)
	}
	invokeArchiveCallback(t, handler, 92, telegramui.ActionArchiveHistory, historyToken)
	history := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(history.Text, "last archived answer") ||
		!strings.Contains(telegramui.CanonicalGrid(history.Grid), "history_prev") {
		t.Fatalf("history=%#v", history)
	}

	olderToken, err := fixture.codec.Page(7, telegramui.ActionHistoryPrevious, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	invokeArchiveCallback(t, handler, 93, telegramui.ActionHistoryPrevious, olderToken)
	older := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(older.Text, "first archived answer") ||
		!strings.Contains(telegramui.CanonicalGrid(older.Grid), "history_next") {
		t.Fatalf("older=%#v", older)
	}
	wrappedToken, err := fixture.codec.Page(7, telegramui.ActionHistoryPrevious, ref, 2)
	if err != nil {
		t.Fatal(err)
	}
	invokeArchiveCallback(t, handler, 94, telegramui.ActionHistoryPrevious, wrappedToken)
	wrapped := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(wrapped.Text, "last archived answer") ||
		!strings.Contains(telegramui.CanonicalGrid(wrapped.Grid), "2/2") {
		t.Fatalf("wrapped history=%#v", wrapped)
	}
}

func addReadyArchive(
	t *testing.T,
	fixture fixture,
	actor application.Principal,
) domain.SessionRef {
	t.Helper()
	created := time.Unix(300, 0).UTC()
	session := domain.Session{
		ID: "archived", NodeID: "allowed", OwnerID: actor.UserID, Name: "Archived",
		Backend: "codex", ProviderSessionID: "provider", Workdir: "/workspace",
		State: domain.SessionLive, RuntimePhase: domain.RuntimeIdle,
		CreatedAt: created, LiveSinceAt: created,
	}
	command, err := clusterstate.NewCommand(
		"add-archive-test", clusterstate.CommandAddSession, created, session,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	stored, err := fixture.service.Session(actor, session.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.CloseSession(
		application.WithOperationScope(context.Background(), "archive-close-test"),
		actor, stored, "archive-test",
	); err != nil {
		t.Fatal(err)
	}
	stored, err = fixture.service.Session(actor, session.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.CompleteSessionArchive(
		application.WithOperationScope(context.Background(), "archive-ready-test"), actor, stored,
	); err != nil {
		t.Fatal(err)
	}
	return session.Ref()
}

func invokeArchiveCallback(
	t *testing.T,
	handler *telegramapp.Handler,
	updateID int64,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) {
	t.Helper()
	data := encodeCallback(t, action, token)
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: updateID, Kind: telegrambot.IncomingCallback,
		UserID: 7, ChatID: 7, CallbackID: "archive", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 20},
	}); err != nil {
		t.Fatal(err)
	}
}
