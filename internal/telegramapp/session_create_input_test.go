package telegramapp_test

import (
	"context"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/sessioncontrol"
	"github.com/Time4Mind/bria/internal/sessionstart"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/workspace"
)

type inputRoutingStarter struct {
	service *application.Service
}

func (s inputRoutingStarter) Browse(
	context.Context,
	application.Principal,
	domain.NodeID,
	string,
) (sessionstart.BrowseResult, error) {
	return sessionstart.BrowseResult{Path: "/work", Directories: []workspace.Directory{
		{Name: "project", Path: "/work/project"},
	}}, nil
}

func (s inputRoutingStarter) Discover(
	context.Context,
	application.Principal,
	domain.NodeID,
	string,
	string,
	int,
	int,
) (sessionstart.ProviderPage, error) {
	return sessionstart.ProviderPage{
		Items: []sessionstart.ProviderCandidate{{ID: "old-provider", Summary: "Previous"}},
		Total: 1,
	}, nil
}

func (s inputRoutingStarter) Create(
	ctx context.Context,
	actor application.Principal,
	request application.CreateSessionRequest,
) (domain.Session, error) {
	return s.service.CreateSession(ctx, actor, request)
}

type activeSessionControls struct {
	*blockingControls
	service       *application.Service
	inputCalls    int
	externalCalls int
}

func (c *activeSessionControls) SendInput(
	_ context.Context,
	actor application.Principal,
	_ string,
	text string,
) (sessioncontrol.Accepted, error) {
	session, err := c.service.ActiveSession(actor)
	if err != nil {
		return sessioncontrol.Accepted{}, err
	}
	c.mu.Lock()
	c.ref = session.Ref()
	c.text = text
	c.inputCalls++
	c.mu.Unlock()
	return sessioncontrol.Accepted{Session: session.Ref()}, nil
}

func (c *activeSessionControls) SendExternalInput(
	_ context.Context,
	actor application.Principal,
	_ string,
	input runtimehost.InputPayload,
) (sessioncontrol.Accepted, error) {
	session, err := c.service.ActiveSession(actor)
	if err != nil {
		return sessioncontrol.Accepted{}, err
	}
	c.mu.Lock()
	c.ref = session.Ref()
	c.external = &input
	c.externalCalls++
	c.mu.Unlock()
	return sessioncontrol.Accepted{Session: session.Ref()}, nil
}

func TestTextOnResumeChoiceStartsFreshInsteadOfUsingOldActiveSession(t *testing.T) {
	fixture := newFixture(t)
	enableCreateBackend(t, fixture)
	oldRef := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &activeSessionControls{
		blockingControls: &blockingControls{ref: oldRef}, service: fixture.service,
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.SetSessionStarter(inputRoutingStarter{service: fixture.service}); err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 10}
	invokeCreateCallback(t, handler, 901, origin, telegramui.ActionNewSession, "")
	invokeCreateCallback(t, handler, 902, origin, telegramui.ActionNewDirectoryPick, "")
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 903, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Text: "new session prompt",
	}); err != nil {
		t.Fatal(err)
	}
	controls.mu.RLock()
	gotRef, gotText := controls.ref, controls.text
	controls.mu.RUnlock()
	if gotRef == oldRef || gotText != "new session prompt" {
		t.Fatalf("routed ref=%s text=%q", gotRef.Key(), gotText)
	}
	active, err := fixture.service.ActiveSession(application.Principal{UserID: 7})
	if err != nil || active.Ref() != gotRef {
		t.Fatalf("active=%s routed=%s err=%v", active.Ref().Key(), gotRef.Key(), err)
	}
}

func TestTextDuringIncompleteCreateFlowNeverFallsThroughToOldSession(t *testing.T) {
	fixture := newFixture(t)
	enableCreateBackend(t, fixture)
	controls := &blockingControls{ref: domain.SessionRef{NodeID: "allowed", SessionID: "live"}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.SetSessionStarter(inputRoutingStarter{service: fixture.service}); err != nil {
		t.Fatal(err)
	}
	invokeCreateCallback(
		t, handler, 904, telegrambot.Message{ChatID: 7, MessageID: 10},
		telegramui.ActionNewSession, "",
	)
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 905, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Text: "must not leak",
	}); err != nil {
		t.Fatal(err)
	}
	controls.mu.RLock()
	defer controls.mu.RUnlock()
	if controls.text != "" {
		t.Fatalf("incomplete create input leaked to old session: %q", controls.text)
	}
	if len(fixture.messenger.sent) != 1 ||
		fixture.messenger.sent[0].Text != "Finish or cancel session creation first. The message was not sent." {
		t.Fatalf("incomplete create input response=%#v", fixture.messenger.sent)
	}
}

func TestSelectingExistingSessionClearsCreateFlowBeforeVoiceInput(t *testing.T) {
	fixture := newFixture(t)
	enableCreateBackend(t, fixture)
	actor := application.Principal{UserID: 7}
	preferences, err := fixture.service.Preferences(actor)
	if err != nil {
		t.Fatal(err)
	}
	preferences.VoiceBackend = domain.VoiceAuto
	if err := fixture.service.SetPreferences(context.Background(), actor, preferences); err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &activeSessionControls{
		blockingControls: &blockingControls{ref: ref}, service: fixture.service,
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.SetSessionStarter(inputRoutingStarter{service: fixture.service}); err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 10}
	invokeCreateCallback(t, handler, 909, origin, telegramui.ActionNewSession, "")
	token, err := fixture.codec.Session(7, telegramui.ActionSelectSession, ref)
	if err != nil {
		t.Fatal(err)
	}
	invokeCreateCallback(t, handler, 910, origin, telegramui.ActionSelectSession, token)
	beforeSent := len(fixture.messenger.sent)
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 911, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Content: telegrambot.ContentDescriptor{
			Kind: telegrambot.IncomingVoice, FileID: "voice-id", FileUniqueID: "voice-unique",
		},
	}); err != nil {
		t.Fatal(err)
	}
	controls.mu.RLock()
	gotRef, gotExternal, gotCalls := controls.ref, controls.external, controls.externalCalls
	gotTotalCalls := controls.inputCalls + controls.externalCalls
	controls.mu.RUnlock()
	if gotCalls != 1 || gotTotalCalls != 1 || gotRef != ref || gotExternal == nil || gotExternal.Kind != runtimehost.InputVoice {
		t.Fatalf("voice routing calls=%d total=%d ref=%s input=%#v", gotCalls, gotTotalCalls, gotRef.Key(), gotExternal)
	}
	if len(fixture.messenger.sent) != beforeSent+1 {
		t.Fatalf("voice input completed silently: sent before=%d after=%d", beforeSent, len(fixture.messenger.sent))
	}
}

func TestSelectingExistingSessionClearsCreateFlowBeforeTextInput(t *testing.T) {
	fixture := newFixture(t)
	enableCreateBackend(t, fixture)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &activeSessionControls{
		blockingControls: &blockingControls{ref: ref}, service: fixture.service,
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.SetSessionStarter(inputRoutingStarter{service: fixture.service}); err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 10}
	invokeCreateCallback(t, handler, 912, origin, telegramui.ActionNewSession, "")
	token, err := fixture.codec.Session(7, telegramui.ActionSelectSession, ref)
	if err != nil {
		t.Fatal(err)
	}
	invokeCreateCallback(t, handler, 913, origin, telegramui.ActionSelectSession, token)
	beforeSent := len(fixture.messenger.sent)
	const input = "continue selected session"
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 914, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7, Text: input,
	}); err != nil {
		t.Fatal(err)
	}
	controls.mu.RLock()
	gotRef, gotText, gotCalls := controls.ref, controls.text, controls.inputCalls
	gotTotalCalls := controls.inputCalls + controls.externalCalls
	controls.mu.RUnlock()
	if gotCalls != 1 || gotTotalCalls != 1 || gotRef != ref || gotText != input {
		t.Fatalf("text routing calls=%d total=%d ref=%s text=%q", gotCalls, gotTotalCalls, gotRef.Key(), gotText)
	}
	if len(fixture.messenger.sent) != beforeSent+1 {
		t.Fatalf("text input completed silently: sent before=%d after=%d", beforeSent, len(fixture.messenger.sent))
	}
}

func TestMenuCancelsIncompleteCreateFlowBeforeNextInput(t *testing.T) {
	fixture := newFixture(t)
	enableCreateBackend(t, fixture)
	oldRef := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &activeSessionControls{
		blockingControls: &blockingControls{ref: oldRef}, service: fixture.service,
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.SetSessionStarter(inputRoutingStarter{service: fixture.service}); err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 10}
	invokeCreateCallback(t, handler, 906, origin, telegramui.ActionNewSession, "")
	invokeCreateCallback(t, handler, 907, origin, telegramui.ActionMenu, "")
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 908, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Text: "must reach the active session",
	}); err != nil {
		t.Fatal(err)
	}
	controls.mu.RLock()
	defer controls.mu.RUnlock()
	if controls.ref != oldRef || controls.text != "must reach the active session" {
		t.Fatalf("routed ref=%s text=%q", controls.ref.Key(), controls.text)
	}
}

func invokeCreateCallback(
	t *testing.T,
	handler *telegramapp.Handler,
	updateID int64,
	origin telegrambot.Message,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) {
	t.Helper()
	data, err := (telegramui.Callback{Action: action, Token: token}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: updateID, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "create", CallbackData: data, CallbackOrigin: origin,
	}); err != nil {
		t.Fatal(err)
	}
}
