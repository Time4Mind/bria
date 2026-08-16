package telegramapp_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/backendsetup"
	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/speechsetup"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type failingBackendSetup struct{ err error }

func (s failingBackendSetup) Start(
	context.Context, backendsetup.Request,
) (backendsetup.Status, error) {
	return backendsetup.Status{}, s.err
}

func (s failingBackendSetup) Status(
	context.Context, backendsetup.Request,
) (backendsetup.Status, error) {
	return backendsetup.Status{}, s.err
}

type machinePort struct {
	machine *clusterstate.Machine
	events  *[]string
}

func (p machinePort) State() *domain.State { return p.machine.State() }
func (p machinePort) Apply(_ context.Context, command clusterstate.Command) (clusterstate.Result, error) {
	if p.events != nil {
		*p.events = append(*p.events, "apply")
	}
	return p.machine.Apply(command), nil
}

type messengerStub struct {
	answers                              []string
	sent                                 []telegramui.Screen
	edited                               []telegramui.Screen
	deleted                              []telegrambot.Message
	events                               *[]string
	editNotify, sendNotify, deleteNotify chan struct{}
}

func (m *messengerStub) AnswerCallbackQuery(_ context.Context, id, text string) error {
	if m.events != nil {
		*m.events = append(*m.events, "answer")
	}
	m.answers = append(m.answers, id+":"+text)
	return nil
}

func (m *messengerStub) SendTyping(context.Context, int64) error { return nil }
func (m *messengerStub) SendDocument(context.Context, telegrambot.DocumentRequest) (telegrambot.Message, error) {
	return telegrambot.Message{ChatID: 7, MessageID: 1}, nil
}
func (m *messengerStub) SendScreen(_ context.Context, _ int64, screen telegramui.Screen) (telegrambot.Message, error) {
	m.sent = append(m.sent, screen)
	if m.sendNotify != nil {
		select {
		case m.sendNotify <- struct{}{}:
		default:
		}
	}
	return stubMessageForScreen(len(m.sent), screen), nil
}
func (m *messengerStub) EditScreen(_ context.Context, message telegrambot.Message, screen telegramui.Screen) (telegrambot.Message, error) {
	if m.events != nil {
		*m.events = append(*m.events, "edit")
	}
	m.edited = append(m.edited, screen)
	if m.editNotify != nil {
		select {
		case m.editNotify <- struct{}{}:
		default:
		}
	}
	return message, nil
}
func (m *messengerStub) DeleteMessage(_ context.Context, message telegrambot.Message) error {
	m.deleted = append(m.deleted, message)
	notifyTest(m.deleteNotify)
	return nil
}
func (m *messengerStub) ClearKeyboard(_ context.Context, message telegrambot.Message) error {
	m.deleted = append(m.deleted, message)
	return nil
}

type fixture struct {
	handler   *telegramapp.Handler
	service   *application.Service
	projector *application.TelegramProjector
	machine   *clusterstate.Machine
	codec     *callbacktoken.Codec
	messenger *messengerStub
	events    *[]string
}

func TestMessageOpensActorMenuAndUnknownActorIsDropped(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 1, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7, Text: "/start",
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.sent) != 1 || fixture.messenger.sent[0].Name != telegramui.ScreenMenu {
		t.Fatalf("sent screens=%#v", fixture.messenger.sent)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 2, Kind: telegrambot.IncomingMessage, ChatID: 99, UserID: 99, Text: "/start",
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.sent) != 1 {
		t.Fatal("unknown actor received a screen")
	}
}

func TestNodeCallbackResolvesOpaqueTokenAndReplicatesSelection(t *testing.T) {
	fixture := newFixture(t)
	token, err := fixture.codec.Node(7, telegramui.ActionSelectNode, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{Action: telegramui.ActionSelectNode, Token: token}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 3, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "callback", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.machine.State().Navigation.ActiveNodeByUser[7]; got != "allowed" {
		t.Fatalf("active node=%q", got)
	}
	if len(fixture.messenger.answers) != 1 || len(fixture.messenger.edited) != 1 ||
		fixture.messenger.edited[0].Name != telegramui.ScreenSessionCard {
		t.Fatalf("answers=%#v edited=%#v", fixture.messenger.answers, fixture.messenger.edited)
	}
}

func TestStatusFromLegacyMenuUsesNewRichCarrier(t *testing.T) {
	fixture := newFixture(t)
	data, err := (telegramui.Callback{Action: telegramui.ActionStatus}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fixture.handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 3, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "status", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.sent) != 1 || !fixture.messenger.sent[0].RichMarkdown ||
		fixture.messenger.sent[0].Name != telegramui.ScreenStatus {
		t.Fatalf("sent=%#v", fixture.messenger.sent)
	}
	if len(fixture.messenger.edited) != 0 {
		t.Fatalf("legacy menu was edited with rich table: %#v", fixture.messenger.edited)
	}
	if len(fixture.messenger.deleted) != 1 || fixture.messenger.deleted[0].MessageID != 10 {
		t.Fatalf("obsolete menu carrier=%#v", fixture.messenger.deleted)
	}
}

func TestSpeechSetupWatcherReconcilesExistingAndNewNodes(t *testing.T) {
	fixture := newFixture(t)
	setup := &speechSetupStub{}
	if err := fixture.handler.SetSpeechSetup(setup); err != nil {
		t.Fatal(err)
	}
	actor := application.Principal{UserID: 7}
	preferences, err := fixture.service.Preferences(actor)
	if err != nil {
		t.Fatal(err)
	}
	preferences.VoiceBackend = domain.VoiceAuto
	if err := fixture.service.SetPreferences(context.Background(), actor, preferences); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		fixture.handler.RunEnrollmentNotifications(ctx, 5*time.Millisecond)
	}()
	deadline := time.Now().Add(time.Second)
	for setup.requestCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if setup.requestCount() != 1 {
		t.Fatal("existing node was not reconciled after handler start")
	}
	newNode := domain.Node{ID: "new-node", Name: "New node", Status: domain.NodeOnline}
	if result := fixture.machine.Apply(commandForTest(
		t, "add-new-node", clusterstate.CommandAddNode, newNode,
	)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	deadline = time.Now().Add(time.Second)
	for setup.requestCount() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if setup.requestCount() != 2 {
		t.Fatal("new node was not provisioned")
	}
	if len(fixture.messenger.sent) != 0 {
		t.Fatalf("automatic provisioning sent progress notifications=%#v", fixture.messenger.sent)
	}
	fixture.messenger.sendNotify = make(chan struct{}, 1)
	setup.setStatus(speechsetup.Status{
		NodeID: "new-node", Engine: "whisper", Phase: speechsetup.PhaseFailed,
		Detail: "ffmpeg is not installed", UpdatedAt: time.Now(),
	})
	select {
	case <-fixture.messenger.sendNotify:
	case <-time.After(time.Second):
		t.Fatal("terminal setup failure was not reported")
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	if len(fixture.messenger.sent) != 1 ||
		!strings.Contains(fixture.messenger.sent[0].Text, "ffmpeg is not installed") {
		t.Fatalf("terminal setup notifications=%#v", fixture.messenger.sent)
	}
}

func TestBackendSetupConnectionFailureIsRenderedWithItsCause(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.handler.SetBackendSetup(failingBackendSetup{
		err: errors.New("relay connection unavailable"),
	}); err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Choice(
		7, telegramui.ActionBackendInstall, "node_backend", "allowed\x00codex",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 22, Kind: telegrambot.IncomingCallback, UserID: 7, ChatID: 7,
		CallbackID:     "backend-install",
		CallbackData:   encodeCallback(t, telegramui.ActionBackendInstall, token),
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
		LanguageCode:   "en",
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 1 || !strings.Contains(
		fixture.messenger.edited[0].Text, "relay connection unavailable",
	) {
		t.Fatalf("backend setup error screen=%#v", fixture.messenger.edited)
	}
	back := fixture.messenger.edited[0].Grid[len(fixture.messenger.edited[0].Grid)-1][0]
	if back.Callback.Action != telegramui.ActionNodeBackend {
		t.Fatalf("backend setup back action=%q", back.Callback.Action)
	}
}

func TestNodeBackendManagementUsesNestedScreens(t *testing.T) {
	fixture := newFixture(t)
	nodeToken, err := fixture.codec.Node(7, telegramui.ActionNodeBackends, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	openToken, err := fixture.codec.Choice(
		7, telegramui.ActionNodeBackend, "node_backend_open", "allowed\x00codex",
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, callback := range []telegramui.Callback{
		{Action: telegramui.ActionNodeBackends, Token: nodeToken},
		{Action: telegramui.ActionNodeBackend, Token: openToken},
	} {
		if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
			UpdateID: 30 + int64(index), Kind: telegrambot.IncomingCallback,
			UserID: 7, ChatID: 7, CallbackID: "backend-menu",
			CallbackData:   encodeCallback(t, callback.Action, callback.Token),
			CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
			LanguageCode:   "en",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(fixture.messenger.edited) != 2 {
		t.Fatalf("backend menu edits=%d", len(fixture.messenger.edited))
	}
	list, detail := fixture.messenger.edited[0], fixture.messenger.edited[1]
	if grid := telegramui.CanonicalGrid(list.Grid); !strings.Contains(grid,
		"[codex · not installed -> node_backend@") {
		t.Fatalf("backend list missing codex:\n%s", grid)
	}
	if grid := telegramui.CanonicalGrid(detail.Grid); !strings.Contains(grid,
		"[install -> backend_install@") || !strings.Contains(detail.Text, "Allowed · codex") {
		t.Fatalf("backend detail invalid:\n%s\n%s", detail.Text, grid)
	}
}

func TestTamperedCallbackCannotResolveHiddenEntity(t *testing.T) {
	fixture := newFixture(t)
	hiddenToken, err := fixture.codec.Node(7, telegramui.ActionSelectNode, "hidden")
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{Action: telegramui.ActionSelectNode, Token: hiddenToken}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 4, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "callback", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 0 || fixture.machine.State().Navigation.ActiveNodeByUser[7] != "allowed" {
		t.Fatal("tampered callback changed or exposed state")
	}
}

func TestSettingsCallbacksReplicateClosedPreferenceChoices(t *testing.T) {
	fixture := newFixture(t)
	callbacks := []telegramui.Callback{
		{Action: telegramui.ActionSetSessionView, Token: "all_hosts"},
		{Action: telegramui.ActionSetResumeSelection, Token: "off"},
		{Action: telegramui.ActionSetToolCalls, Token: "off"},
		{Action: telegramui.ActionSetToolResults, Token: "off"},
		{Action: telegramui.ActionSetToolOutputLines, Token: "25"},
		{Action: telegramui.ActionSetThinking, Token: "off"},
		{Action: telegramui.ActionSetResponseCards, Token: "keep_latest"},
		{Action: telegramui.ActionSetIdleArchive, Token: "unlimited"},
		{Action: telegramui.ActionSetRetention, Token: "30"},
		{Action: telegramui.ActionSetExpiry, Token: "all"},
	}
	for index, callback := range callbacks {
		data, err := callback.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
			UpdateID: int64(20 + index), Kind: telegrambot.IncomingCallback,
			ChatID: 7, UserID: 7, CallbackID: "settings", CallbackData: data,
			CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
		}); err != nil {
			t.Fatal(err)
		}
	}
	preferences := fixture.machine.State().Preferences[7]
	if preferences.SessionView != domain.ViewAllHosts || preferences.IdleArchiveHours != 0 ||
		preferences.ArchiveRetentionDays != 30 || preferences.ArchiveExpiryAction != domain.ArchiveRemoveAll ||
		preferences.ResponseCards != domain.ResponseCardsKeepLatest || !preferences.SkipResumeSelection {
		t.Fatalf("preferences=%#v", preferences)
	}
	if preferences.ShowsAllTechnicalCardEvents() || len(preferences.HiddenCardEvents) != 3 {
		t.Fatalf("technical card visibility=%#v", preferences.HiddenCardEvents)
	}
	if preferences.EffectiveToolOutputLines() != 25 {
		t.Fatalf("tool output lines=%d", preferences.EffectiveToolOutputLines())
	}
	if len(fixture.messenger.edited) != len(callbacks) ||
		fixture.messenger.edited[len(callbacks)-1].Name != telegramui.ScreenSettings {
		t.Fatalf("settings edits=%#v", fixture.messenger.edited)
	}
}

func TestSettingsCallbackStopsTelegramSpinnerBeforeRaftApply(t *testing.T) {
	fixture := newFixture(t)
	*fixture.events = (*fixture.events)[:0]
	data, err := (telegramui.Callback{
		Action: telegramui.ActionSetToolCalls, Token: "off",
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 39, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "settings", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"answer", "apply", "edit"}
	if got := *fixture.events; !slices.Equal(got, want) {
		t.Fatalf("callback event order=%v, want %v", got, want)
	}
}

func TestFirstMessagePersistsTelegramLanguageAndRendersLocalizedMenu(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 40, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		LanguageCode: "ru-RU", Text: "/start",
	}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.machine.State().Preferences[7].Language; got != domain.LanguageRussian {
		t.Fatalf("language=%q", got)
	}
	if len(fixture.messenger.sent) != 1 ||
		!strings.HasPrefix(fixture.messenger.sent[0].Text, "Меню · активная: Live") {
		t.Fatalf("sent=%#v", fixture.messenger.sent)
	}
}

func TestSettingsNavigationAndLanguageSwitchStayInSameCard(t *testing.T) {
	fixture := newFixture(t)
	openCategory := telegramui.Callback{
		Action: telegramui.ActionSettingsCategory, Token: "interface",
	}
	openData, err := openCategory.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 50, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "open", CallbackData: openData,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 1 ||
		!strings.Contains(fixture.messenger.edited[0].Text, "Interface and language") {
		t.Fatalf("category edit=%#v", fixture.messenger.edited)
	}

	setLanguage := telegramui.Callback{Action: telegramui.ActionSetLanguage, Token: "ru"}
	languageData, err := setLanguage.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 51, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "language", CallbackData: languageData,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.machine.State().Preferences[7].Language; got != domain.LanguageRussian {
		t.Fatalf("language=%q", got)
	}
	last := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.HasPrefix(last.Text, "<b>Язык</b>\n") ||
		!strings.Contains(telegramui.CanonicalGrid(last.Grid), "• Русский") {
		t.Fatalf("localized setting=%#v", last)
	}
	if got := fixture.messenger.answers[len(fixture.messenger.answers)-1]; got != "language:" {
		t.Fatalf("callback answer=%q", got)
	}
}
