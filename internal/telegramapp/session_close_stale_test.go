package telegramapp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/sessioncontrol"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (c *closingControls) Close(
	ctx context.Context,
	actor application.Principal,
	_ string,
	ref domain.SessionRef,
) (sessioncontrol.Accepted, error) {
	if err := c.service.RequireSessionAction(actor, ref, domain.ActionClose); err != nil {
		return sessioncontrol.Accepted{}, err
	}
	session, err := c.service.Session(actor, ref)
	if err != nil {
		return sessioncontrol.Accepted{}, err
	}
	err = c.service.CloseSession(
		application.WithOperationScope(ctx, "test-close-"+ref.Key()), actor, session,
		"archive-"+string(ref.SessionID),
	)
	return sessioncontrol.Accepted{Session: ref}, err
}

func TestStaleCloseButtonRefreshesArchivedSessionInsteadOfConfirming(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	archiveFixtureSession(t, fixture, actor, ref)
	controls := &blockingControls{ref: ref}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Session(7, telegramui.ActionClose, ref)
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{Action: telegramui.ActionClose, Token: token}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 72, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "stale-close", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	assertArchivedSessionRefresh(t, fixture, "stale-close:")
}

func TestStaleCloseConfirmationRefreshesArchivedSession(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	archiveFixtureSession(t, fixture, actor, ref)
	controls := &closingControls{
		blockingControls: &blockingControls{ref: ref}, service: fixture.service,
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Session(7, telegramui.ActionConfirmClose, ref)
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{
		Action: telegramui.ActionConfirmClose, Token: token,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 73, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "stale-confirm", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	assertArchivedSessionRefresh(t, fixture, "stale-confirm:")
}

func archiveFixtureSession(
	t *testing.T,
	fixture fixture,
	actor application.Principal,
	ref domain.SessionRef,
) {
	t.Helper()
	session, err := fixture.service.Session(actor, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.CloseSession(
		application.WithOperationScope(context.Background(), "archive-stale-fixture"),
		actor, session, "archive-stale-fixture",
	); err != nil {
		t.Fatal(err)
	}
}

func assertArchivedSessionRefresh(t *testing.T, fixture fixture, answer string) {
	t.Helper()
	if len(fixture.messenger.answers) != 1 || fixture.messenger.answers[0] != answer {
		t.Fatalf("answers=%#v", fixture.messenger.answers)
	}
	if len(fixture.messenger.edited) != 1 {
		t.Fatalf("edits=%#v", fixture.messenger.edited)
	}
	screen := fixture.messenger.edited[0]
	if screen.Name != telegramui.ScreenSessionCard {
		t.Fatalf("screen=%#v", screen)
	}
	grid := telegramui.CanonicalGrid(screen.Grid)
	if strings.Contains(grid, string(telegramui.ActionClose)) ||
		strings.Contains(grid, string(telegramui.ActionConfirmClose)) {
		t.Fatalf("stale close controls remained in %q", grid)
	}
}
