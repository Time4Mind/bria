package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/screen"
	"bria/internal/screenproduction"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramruntimecomposition"
	"bria/internal/telegramui"
)

// Only time, the local native snapshot and HTTP are fake. Ingress, controller,
// signed projection, source/render, scheduling and Rich delivery are real.
func TestRefreshCadenceJoinedRichWireElapsedStages(t *testing.T) {
	f := newRefreshCadenceFixture(t)
	f.action(coordinator.Update{ID: 1, Kind: coordinator.UpdateMessage, Text: "/menu"})
	f.send("initial", 0)
	// Expectations are literal contract boundaries, not the scheduler algorithm.
	for _, stage := range []struct {
		at, interval time.Duration
	}{
		{10 * time.Second, 1500 * time.Millisecond},
		{25 * time.Second, 2 * time.Second},
		{50 * time.Second, 2500 * time.Millisecond},
		{650 * time.Second, 3 * time.Second},
		{1850 * time.Second, 3500 * time.Millisecond},
		{3050 * time.Second, 4 * time.Second},
	} {
		f.clock.set(f.start.Add(stage.at))
		f.send(fmt.Sprintf("stage %s anchor", stage.at), 0)
		f.send(fmt.Sprintf("stage %s next", stage.at), stage.interval)
	}
}

func TestRefreshCadenceJoinedIngressResetsBothTextAndImage(t *testing.T) {
	for _, action := range []struct {
		name   string
		update coordinator.Update
	}{
		{"message", coordinator.Update{Kind: coordinator.UpdateMessage, Text: "/menu"}},
		{"voice", coordinator.Update{Kind: coordinator.UpdateMessage, MediaKind: "voice"}},
		{"empty_noop", coordinator.Update{Kind: coordinator.UpdateMessage}},
		{"stale_owner_callback", coordinator.Update{Kind: coordinator.UpdateCallback, Text: "expired-button"}},
		{"signed_page_noop", coordinator.Update{Kind: coordinator.UpdateCallback}},
	} {
		t.Run(action.name, func(t *testing.T) {
			f := newRefreshCadenceFixture(t)
			f.action(coordinator.Update{ID: 1, Kind: coordinator.UpdateMessage, Text: "/menu"})
			f.send("initial", 0)
			f.clock.set(f.start.Add(1850 * time.Second))
			f.send("old cadence anchor", 0)
			f.send("background native change", 3500*time.Millisecond)
			action.update.ID = 2
			if action.update.Kind == coordinator.UpdateCallback {
				action.update.CallbackQueryID = "cadence-owner-query"
			}
			if action.name == "signed_page_noop" {
				packet := f.wire.packets[len(f.wire.packets)-1]
				for _, row := range packet.keyboard.InlineKeyboard {
					for _, button := range row {
						decoded, err := f.presenter.DecodeCallback(button.CallbackData)
						if err == nil && decoded.Action == telegramui.ActionPageLatest {
							action.update.Text = button.CallbackData
						}
					}
				}
				if action.update.Text == "" {
					t.Fatal("signed page button absent on actual wire")
				}
			}
			lastCard := f.clock.now()
			if err := f.action(action.update); err != nil && action.name == "signed_page_noop" {
				t.Fatalf("actual signed page callback failed: %v", err)
			}
			reset := f.clock.now()
			f.send("fresh after user action", 1500*time.Millisecond-reset.Sub(lastCard))
			f.clock.set(reset.Add(60 * time.Second))
			f.send("aged again", 0)
			// Replayed update and an unauthenticated actor cannot reset the timer.
			lastCard = f.clock.now()
			f.action(action.update)
			f.send("duplicate did not reset", 2500*time.Millisecond-f.clock.now().Sub(lastCard))
			foreign := coordinator.Update{ID: 3, Kind: coordinator.UpdateMessage, ActorID: 8, Text: "/menu"}
			f.action(foreign)
			f.send("foreign actor did not reset", 2500*time.Millisecond)
		})
	}
}

type refreshCadenceClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *refreshCadenceClock) now() time.Time   { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *refreshCadenceClock) set(at time.Time) { c.mu.Lock(); c.at = at; c.mu.Unlock() }
func (c *refreshCadenceClock) wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
	return nil
}

type refreshCadenceNative struct{ text string }

func (n *refreshCadenceNative) NativeScreen(domain.SessionID) (sessionruntime.NativeSnapshot, bool) {
	return sessionruntime.NativeSnapshot{FullText: n.text, Hash: n.text}, true
}
func (*refreshCadenceNative) NativeScreenUpdates() <-chan domain.SessionID { return nil }

type refreshCadencePacket struct {
	at       time.Time
	text     string
	png      []byte
	keyboard telegram.InlineKeyboardMarkup
}
type refreshCadenceHTTP struct {
	t       *testing.T
	clock   *refreshCadenceClock
	packets []refreshCadencePacket
}

func (h *refreshCadenceHTTP) Do(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/answerCallbackQuery") {
		return telegramResponse(`{"ok":true,"result":true}`), nil
	}
	if !strings.HasSuffix(req.URL.Path, "/editMessageText") {
		h.t.Fatalf("unexpected independent transport %s", req.URL.Path)
	}
	if err := req.ParseMultipartForm(2 << 20); err != nil {
		h.t.Fatal(err)
	}
	defer req.MultipartForm.RemoveAll()
	var rich telegram.InputRichMessage
	if err := json.Unmarshal([]byte(req.FormValue("rich_message")), &rich); err != nil {
		h.t.Fatal(err)
	}
	if req.FormValue("chat_id") != "42" || req.FormValue("message_id") != "91" || len(rich.Media) != 1 {
		h.t.Fatalf("wrong joined card identity/media: %+v", rich)
	}
	file, _, err := req.FormFile(telegram.ScreenPhotoID)
	if err != nil {
		h.t.Fatal(err)
	}
	defer file.Close()
	png, err := io.ReadAll(file)
	if err != nil {
		h.t.Fatal(err)
	}
	var keyboard telegram.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(req.FormValue("reply_markup")), &keyboard); err != nil {
		h.t.Fatal(err)
	}
	h.packets = append(h.packets, refreshCadencePacket{h.clock.now(), rich.Markdown, png, keyboard})
	return telegramResponse(`{"ok":true,"result":{"message_id":91,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
}

type refreshCadenceFixture struct {
	t         *testing.T
	start     time.Time
	clock     *refreshCadenceClock
	native    *refreshCadenceNative
	wire      *refreshCadenceHTTP
	handler   *telegramflow.Handler
	outbound  *telegramflow.Sender
	presenter *telegrambridge.Presenter
	id        domain.SessionID
	sequence  int
}

func newRefreshCadenceFixture(t *testing.T) *refreshCadenceFixture {
	t.Helper()
	ctx := context.Background()
	start := time.Unix(1_800_000_000, 0).UTC()
	clock := &refreshCadenceClock{at: start}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.now, Wait: clock.wait, Jitter: func(time.Duration) time.Duration { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	store, sessions := archiveFixture(t, "11111111-1111-4111-9111-111111111111")
	if err := store.SetActiveSession(ctx, sessions[0].ID()); err != nil {
		t.Fatal(err)
	}
	c := archiveController(t, store, nil, telegramcontroller.Options{Recovered: sessions, UIState: store})
	t.Cleanup(func() { _ = c.Close(ctx) })
	prefs := settings.NewMemoryStore()
	if err := prefs.Update(ctx, func(s *settings.Settings) error { s.ScreenEnabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	native := &refreshCadenceNative{}
	source, err := screenproduction.NewNativeSource(prefs, store, native)
	if err != nil {
		t.Fatal(err)
	}
	wire := &refreshCadenceHTTP{t: t, clock: clock}
	client, err := telegram.NewClient("123:cadence-synthetic", wire, telegram.Options{Scheduler: scheduler})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sender.Close(ctx) })
	if err := sender.BindScreenSource(source); err != nil {
		t.Fatal(err)
	}
	codec, err := callbacktoken.New(bytes.Repeat([]byte{42}, 32), nil, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, clock.now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	adapter := telegramruntimecomposition.ControllerFlowAdapter{Controller: c}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(clock.now),
		UIState:          telegramruntimecomposition.SessionTelegramUIStore{State: store},
		MessageUI:        adapter, Callbacks: adapter, Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: sender,
		OnUserAction: func(u coordinator.Update) { scheduler.RecordUserActivity(u.ConversationID, u.ID) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &refreshCadenceFixture{t: t, start: start, clock: clock, native: native, wire: wire, handler: handler, outbound: outbound, presenter: presenter, id: sessions[0].ID()}
}

func (f *refreshCadenceFixture) action(u coordinator.Update) error {
	f.t.Helper()
	if u.ActorID == 0 {
		u.ActorID = 7
	}
	u.ConversationID, u.ConversationKind, u.SourceMessageID = 42, "private", 91
	// Invalid/stale callbacks still count as owner ingress before decoding.
	_, err := f.handler.Handle(context.Background(), u)
	return err
}

func (f *refreshCadenceFixture) send(text string, delay time.Duration) {
	f.t.Helper()
	ctx := context.Background()
	f.sequence++
	f.native.text = text
	wantPNG, err := screen.RenderNative(ctx, text)
	if err != nil {
		f.t.Fatal(err)
	}
	input := telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: text, Anchors: []string{"history:1"}}}, View: telegramui.PageView{Page: 1, Pages: 1}, Keyboard: telegramui.CardKeyboardInput{View: telegramui.PageView{Page: 1, Pages: 1}}}
	prepared, err := telegramflow.PrepareCardRefresh(fmt.Sprintf("cadence:%d", f.sequence), f.id, 42, 91, input, "native status", false, nil, f.presenter)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.outbound.Register(prepared); err != nil {
		f.t.Fatal(err)
	}
	before, count := f.clock.now(), len(f.wire.packets)
	receipt, err := f.outbound.EditStatusWithKeyboard(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard)
	if err != nil || receipt.MessageID != 91 {
		f.t.Fatalf("joined refresh receipt=%+v err=%v", receipt, err)
	}
	if len(f.wire.packets) != count+1 {
		f.t.Fatal("refresh did not emit exactly one joined packet")
	}
	got := f.wire.packets[count]
	if got.at.Sub(before) != delay || !strings.Contains(got.text, text) || !bytes.Equal(got.png, wantPNG) {
		f.t.Fatalf("joined refresh %q: delay=%s want=%s text=%q currentPNG=%t", text, got.at.Sub(before), delay, got.text, bytes.Equal(got.png, wantPNG))
	}
}
