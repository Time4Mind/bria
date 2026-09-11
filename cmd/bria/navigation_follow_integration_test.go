package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcompletioncomposition"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramnotify"
	"bria/internal/telegrampipeline"
	"bria/internal/telegrampromptcomposition"
	"bria/internal/telegramruntimecomposition"
	"bria/internal/telegramui"
)

// Only HTTP and the dispatch barrier are synthetic. Navigation, projection,
// persisted history/page plans, signed callbacks and delivery commits are real.
func TestNavigationFollowJoinedDeferredUpdatesCannotOverwriteOtherView(t *testing.T) {
	for _, nodes := range []bool{false, true} {
		for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationCommentary} {
			t.Run(fmt.Sprintf("nodes=%t/%s", nodes, kind), func(t *testing.T) {
				f := newNavigationFollowFixture(t)
				gate := &navigationFollowGate{Sender: f.out, entered: make(chan struct{}), release: make(chan struct{}, 1)}
				defer close(gate.release)
				prompt, completion := f.prompt, f.completion
				prompt.Sender, completion.Sender = gate, gate
				done := make(chan error, 1)
				go func() {
					n := telegramcontroller.Notification{Kind: kind, SessionID: f.id}
					var err error
					if kind == telegramcontroller.NotificationPromptStatus {
						_, err = prompt.Deliver(f.ctx, n, "deferred:prompt")
					} else {
						_, err = completion.Deliver(f.ctx, n, "deferred:commentary")
					}
					done <- err
				}()
				select {
				case <-gate.entered:
				case err := <-done:
					t.Fatalf("visible update never reached dispatch barrier: %v", err)
				case <-f.ctx.Done():
					t.Fatal("dispatch barrier not reached")
				}
				f.message("/menu")
				if nodes {
					f.click(telegramui.ActionMenuStatus) // The menu's visible "Ноды" button.
				}
				before := f.wire.snapshot()
				gate.release <- struct{}{}
				select {
				case <-done: // Cancellation may be reported or intentionally suppressed.
				case <-f.ctx.Done():
					t.Fatal("invalidated update did not finish")
				}
				if after := f.wire.snapshot(); len(after) != len(before) {
					t.Fatalf("deferred %s overwrote navigation: mutations %d -> %d", kind, len(before), len(after))
				}
				// Notifications generated after navigation must also stay hidden.
				f.deliver(kind, "after-navigation")
				if len(f.wire.snapshot()) != len(before) {
					t.Fatal("fresh background status overwrote the selected view")
				}
			})
		}
	}
}

func TestNavigationFollowJoinedLatestAndPinnedGrowth(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(fmt.Sprintf("pinned=%t", pinned), func(t *testing.T) {
			f := newNavigationFollowFixture(t)
			f.click(telegramui.ActionPageLatest)
			if pinned {
				f.click(telegramui.ActionPagePrevious)
			}
			before := sessionPageBody(f.wire.last().Text)
			state, err := f.store.LoadTelegramUI(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			old, _ := state.Card(f.id)
			if old.Page.Total < 2 || pinned && old.Page.FollowLatest {
				t.Fatal("fixture must select a genuinely earlier page of a multi-page transcript")
			}
			f.append("commentary", strings.Repeat("new growing line\n", 300)+"GROWTH_TAIL")
			f.deliver(telegramcontroller.NotificationPromptStatus, "growth")
			got := sessionPageBody(f.wire.last().Text)
			if pinned && got != before {
				t.Fatal("earlier page changed during growth")
			}
			if !pinned && !strings.Contains(got, "GROWTH_TAIL") {
				t.Fatal("latest page stopped following transcript growth")
			}
			state, err = f.store.LoadTelegramUI(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			now, _ := state.Card(f.id)
			if now.Page.Total <= old.Page.Total || pinned && now.Page.Current != old.Page.Current || !pinned && now.Page.Current != now.Page.Total {
				t.Fatal("durable page plan lost pinned/latest selection during growth")
			}
		})
	}
}

func sessionPageBody(text string) string {
	const separator = "─────  \n"
	if _, body, found := strings.Cut(text, separator); found {
		return body
	}
	return text
}

func TestNavigationFollowJoinedEveryInputStartsLatestCarrierAndFreezesPrevious(t *testing.T) {
	f := newNavigationFollowFixture(t)
	f.click(telegramui.ActionPageLatest)
	f.click(telegramui.ActionPagePrevious)
	before := f.wire.snapshot()
	old := before[len(before)-1]
	state, err := f.store.LoadTelegramUI(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	pinned, _ := state.Card(f.id)
	if pinned.Page.FollowLatest || pinned.Page.Current == pinned.Page.Total {
		t.Fatal("fixture did not pin an earlier page")
	}

	f.message("VISIBLE_NEW_INPUT")
	afterInput := f.wire.snapshot()
	current := requireRetireThenNewRichCard(t, afterInput, len(before), old.ID)
	if current.ID == old.ID || !strings.Contains(current.Text, "VISIBLE_NEW_INPUT") {
		t.Fatalf("new input carrier = %+v", current)
	}
	state, err = f.store.LoadTelegramUI(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	card, _ := state.Card(f.id)
	if card.Carrier.MessageID != current.ID || card.Page.Current != card.Page.Total || !card.Page.FollowLatest {
		t.Fatalf("new input page state = %+v", card)
	}

	f.append("commentary", "MODEL_AFTER_NEW_INPUT")
	f.deliver(telegramcontroller.NotificationCommentary, "after-new-input")
	packets := f.wire.snapshot()
	last := packets[len(packets)-1]
	if last.ID != current.ID || !strings.Contains(last.Text, "MODEL_AFTER_NEW_INPUT") {
		t.Fatalf("model update did not target newest carrier: %+v", last)
	}
	for _, packet := range packets[len(afterInput):] {
		if packet.ID == old.ID {
			t.Fatalf("previous carrier received a later mutation: %+v", packet)
		}
	}
}

func TestNavigationFollowJoinedFinalNewCarrierStartsAnswer(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, long := range []bool{false, true} {
			t.Run(fmt.Sprintf("pinned=%t/long=%t", pinned, long), func(t *testing.T) {
				f := newNavigationFollowFixture(t)
				f.click(telegramui.ActionPageLatest)
				if pinned {
					f.click(telegramui.ActionPagePrevious)
				}
				old := f.wire.last()
				answer := "FINAL_ANSWER_BEGIN\n"
				if long {
					answer += strings.Repeat("final answer continuation\n", 140)
				}
				answer += "FINAL_ANSWER_END"
				f.append("final", answer)
				session, err := f.store.Load(f.ctx, f.id)
				if err != nil {
					t.Fatal(err)
				}
				ready, err := session.FinishWork(time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if err := f.store.Replace(f.ctx, session, ready); err != nil {
					t.Fatal(err)
				}
				before := f.wire.snapshot()
				f.deliver(telegramcontroller.NotificationFinal, "exact-final")
				packets := f.wire.snapshot()
				got := requireRetireThenNewRichCard(t, packets, len(before), old.ID)
				if got.ID == old.ID {
					t.Fatal("final reused the old carrier")
				}
				if !strings.Contains(got.Text, "FINAL_ANSWER_BEGIN") || long && strings.Contains(got.Text, "FINAL_ANSWER_END") {
					t.Fatal("new final card does not open at the beginning of the answer")
				}
				// The old card keeps its text but loses its keyboard before the
				// one distinct Rich card is sent.
				state, err := f.store.LoadTelegramUI(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				card, _ := state.Card(f.id)
				if card.Carrier.MessageID != got.ID {
					t.Fatal("new final carrier receipt was not persisted")
				}
			})
		}
	}
}

type navigationFollowPacket struct {
	ID                     int64
	Method, Text, Keyboard string
}

func requireRetireThenNewRichCard(t *testing.T, packets []navigationFollowPacket, start int, retiredID int64) navigationFollowPacket {
	t.Helper()
	if start < 0 || start > len(packets) {
		t.Fatalf("invalid Telegram packet start %d for %d packets", start, len(packets))
	}
	delta := packets[start:]
	sendIndex, sends := -1, 0
	for i, packet := range delta {
		if packet.Method == "sendRichMessage" {
			sendIndex, sends = i, sends+1
		}
	}
	if sends != 1 {
		t.Fatalf("new Rich cards = %d, want exactly one; mutations=%+v", sends, delta)
	}
	if sendIndex == 0 {
		t.Fatalf("new Rich card was not preceded by keyboard retirement: %+v", delta)
	}
	retirement := delta[sendIndex-1]
	if retirement.Method != "editMessageReplyMarkup" || retirement.ID != retiredID {
		t.Fatalf("mutation before new Rich card = %+v, want keyboard retirement for %d", retirement, retiredID)
	}
	var keyboard struct {
		InlineKeyboard []json.RawMessage `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(retirement.Keyboard), &keyboard); err != nil || keyboard.InlineKeyboard == nil || len(keyboard.InlineKeyboard) != 0 {
		t.Fatalf("retired keyboard = %q, want explicit empty inline keyboard", retirement.Keyboard)
	}
	return delta[sendIndex]
}

func countRichMessages(packets []navigationFollowPacket) int {
	count := 0
	for _, packet := range packets {
		if packet.Method == "sendRichMessage" {
			count++
		}
	}
	return count
}

type navigationFollowHTTP struct {
	mu      sync.Mutex
	packets []navigationFollowPacket
	next    int64
}

func (h *navigationFollowHTTP) Do(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if method == "answerCallbackQuery" {
		return telegramResponse(`{"ok":true,"result":true}`), nil
	}
	var p struct {
		MessageID int64                      `json:"message_id"`
		Text      string                     `json:"text"`
		Rich      *telegram.InputRichMessage `json:"rich_message"`
		Keyboard  json.RawMessage            `json:"reply_markup"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		return nil, err
	}
	if p.Rich != nil {
		p.Text = p.Rich.Markdown
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if p.MessageID == 0 {
		h.next++
		p.MessageID = h.next
	}
	h.packets = append(h.packets, navigationFollowPacket{p.MessageID, method, p.Text, string(p.Keyboard)})
	return telegramResponse(fmt.Sprintf(`{"ok":true,"result":{"message_id":%d,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`, p.MessageID)), nil
}
func (h *navigationFollowHTTP) snapshot() []navigationFollowPacket {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]navigationFollowPacket(nil), h.packets...)
}
func (h *navigationFollowHTTP) last() navigationFollowPacket { p := h.snapshot(); return p[len(p)-1] }

type navigationFollowGate struct {
	*telegramflow.Sender
	entered, release chan struct{}
}

func (g *navigationFollowGate) EditStatusWithKeyboard(ctx context.Context, op string, s coordinator.Status, k *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	close(g.entered)
	select {
	case <-g.release:
	case <-ctx.Done():
		return coordinator.Receipt{}, ctx.Err()
	}
	return g.Sender.EditStatusWithKeyboard(ctx, op, s, k)
}

type navigationFollowFixture struct {
	t          *testing.T
	ctx        context.Context
	id         domain.SessionID
	store      *storage.SessionStore
	wire       *navigationFollowHTTP
	handler    *telegramflow.Handler
	out        *telegramflow.Sender
	presenter  *telegrambridge.Presenter
	prompt     telegrampromptcomposition.Deliverer
	completion telegramcompletioncomposition.CompletionDeliverer
	seq        int64
}

func newNavigationFollowFixture(t *testing.T) *navigationFollowFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	store, sessions := archiveFixture(t, "11111111-1111-4111-9111-111111111111")
	id := sessions[0].ID()
	running, err := sessions[0].StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(ctx, sessions[0], running); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActiveSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardCarrier(ctx, id, 42, 91); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCardTypedHistory(ctx, id, "HISTORY_BEGIN\n"+strings.Repeat("prior transcript line\n", 300)+"HISTORY_TAIL", "commentary"); err != nil {
		t.Fatal(err)
	}
	c := archiveController(t, store, nil, telegramcontroller.Options{Recovered: []domain.Session{running}, UIState: store})
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: id}); err != nil {
		t.Fatal(err)
	}
	wire := &navigationFollowHTTP{next: 100}
	client, err := telegram.NewClient("123:navigation-synthetic", wire, telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	base, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close(context.Background()) })
	codec, err := callbacktoken.New(bytes.Repeat([]byte{29}, 32), nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, time.Now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	adapter := telegramruntimecomposition.ControllerFlowAdapter{Controller: c}
	cards := telegramruntimecomposition.SessionTelegramUIStore{State: store}
	handler, out, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(time.Now), UIState: cards, MessageUI: adapter, Callbacks: adapter, Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: base})
	if err != nil {
		t.Fatal(err)
	}
	f := &navigationFollowFixture{t: t, ctx: ctx, id: id, store: store, wire: wire, handler: handler, out: out, presenter: presenter}
	f.prompt = telegrampromptcomposition.Deliverer{Controller: c, Cards: cards, Presenter: presenter, Sender: out}
	f.completion = telegramcompletioncomposition.CompletionDeliverer{Controller: c, Cards: cards, Presenter: presenter, Sender: out, ConversationID: 42}
	f.deliver(telegramcontroller.NotificationPromptStatus, "initial")
	return f
}
func (f *navigationFollowFixture) append(kind, text string) {
	f.t.Helper()
	if err := f.store.AppendCardTypedHistory(f.ctx, f.id, text, kind); err != nil {
		f.t.Fatal(err)
	}
}
func (f *navigationFollowFixture) deliver(kind telegramcontroller.NotificationKind, op string) {
	f.t.Helper()
	n := telegramcontroller.Notification{Kind: kind, SessionID: f.id, ConversationID: 42}
	var r telegramnotify.DeliveryReceipt
	var err error
	if kind == telegramcontroller.NotificationPromptStatus {
		r, err = f.prompt.Deliver(f.ctx, n, op)
	} else {
		r, err = f.completion.Deliver(f.ctx, n, op)
	}
	if err != nil || r.State != telegramnotify.DeliveryConfirmed {
		f.t.Fatalf("%s delivery state=%s err=%v", kind, r.State, err)
	}
}
func (f *navigationFollowFixture) message(text string) {
	f.t.Helper()
	f.action(coordinator.Update{Kind: coordinator.UpdateMessage, Text: text})
}
func (f *navigationFollowFixture) click(action telegramui.Action) {
	f.t.Helper()
	var k telegram.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(f.wire.last().Keyboard), &k); err != nil {
		f.t.Fatal(err)
	}
	for _, row := range k.InlineKeyboard {
		for _, b := range row {
			decoded, err := f.presenter.DecodeCallback(b.CallbackData)
			if err == nil && decoded.Action == action {
				f.action(coordinator.Update{Kind: coordinator.UpdateCallback, Text: b.CallbackData, SourceMessageID: f.wire.last().ID, CallbackQueryID: fmt.Sprintf("query-%d", f.seq+1)})
				return
			}
		}
	}
	f.t.Fatalf("actual keyboard lacks %s", action)
}
func (f *navigationFollowFixture) action(u coordinator.Update) {
	f.t.Helper()
	f.seq++
	u.ID, u.ActorID, u.ConversationID, u.ConversationKind = f.seq, 7, 42, "private"
	d, err := f.handler.Handle(f.ctx, u)
	if err != nil {
		f.t.Fatal(err)
	}
	if d.Kind != coordinator.DecisionStatus {
		f.t.Fatalf("navigation skipped: %s", d.Kind)
	}
	op := fmt.Sprintf("status:%d", u.ID)
	if d.Status.SourceMessageID > 0 {
		_, err = f.out.EditStatusWithKeyboard(f.ctx, op, d.Status, d.Keyboard)
	} else {
		_, err = f.out.SendStatusWithKeyboard(f.ctx, op, d.Status, d.Keyboard)
	}
	if err != nil {
		f.t.Fatal(err)
	}
}
