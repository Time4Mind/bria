package telegramcompletioncomposition

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/domain"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramstate"
)

type spacingHTTPClient func(*http.Request) (*http.Response, error)

func (client spacingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return client(request)
}

type spacingSender struct{ *telegrambridge.Sender }

func (*spacingSender) Register(telegramflow.Prepared) error { return nil }

func TestCompletionDividerHasOneNewlineOnSendAndEditWire(t *testing.T) {
	const header = "Session · local · codex · ready\n\n─────  \n"
	for _, body := range []struct{ name, markdown, text string }{
		{"ordinary", "**Answer**\n\nAnother paragraph", "Answer\n\nAnother paragraph"},
		{"code", "```go\nanswer()\n```", "answer()\n"},
		{"list", "- **First**\n- Second", "- First\n- Second"},
	} {
		for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationFinal, telegramcontroller.NotificationCommentary} {
			t.Run(body.name+"/"+string(kind), func(t *testing.T) {
				ctx := context.Background()
				now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
				codec, err := callbacktoken.New(bytes.Repeat([]byte{8}, 32), bytes.NewReader(make([]byte, 4096)), func() time.Time { return now })
				if err != nil {
					t.Fatal(err)
				}
				presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				id := domain.SessionID("00000000-0000-4000-8000-000000000001")
				store := telegramstate.NewMemoryStore()
				if err := store.Update(ctx, func(state *telegramstate.State) error {
					state.ActiveSession = id
					return state.SetCard(telegramstate.Card{SessionID: id, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 55}, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}})
				}); err != nil {
					t.Fatal(err)
				}
				var wire struct {
					Text      string                     `json:"text"`
					MessageID int64                      `json:"message_id"`
					Rich      *telegram.InputRichMessage `json:"rich_message"`
				}
				var method string
				client, err := telegram.NewClient("123:spacing-test", spacingHTTPClient(func(request *http.Request) (*http.Response, error) {
					method = request.URL.Path
					if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
						t.Fatal(err)
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":55,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`))}, nil
				}), telegram.Options{})
				if err != nil {
					t.Fatal(err)
				}
				sender, err := telegrambridge.NewSender(client)
				if err != nil {
					t.Fatal(err)
				}
				card := telegramcontroller.SemanticCard{
					SessionID: id, Header: header,
					Pages:                []telegramcontroller.SemanticContentPage{{Content: body.markdown, Anchors: []string{"latest"}}},
					View:                 telegramcontroller.SemanticPageView{Page: 1, Pages: 1, Anchor: "latest", FollowLatest: true},
					SelectableSessionIDs: []domain.SessionID{id}, SessionRowSizes: []int{1},
				}
				deliverer := CompletionDeliverer{Controller: completionControllerStub{card: card, active: true}, Presenter: presenter, Sender: &spacingSender{sender}, Cards: store, ConversationID: 42}
				receipt, err := deliverer.Deliver(ctx, telegramcontroller.Notification{Kind: kind, SessionID: id}, "spacing:wire")
				if err != nil || receipt.State != "confirmed" {
					t.Fatalf("delivery = (%#v, %v)", receipt, err)
				}
				wantMethod := "/bot123:spacing-test/sendRichMessage"
				if kind == telegramcontroller.NotificationCommentary {
					wantMethod = "/bot123:spacing-test/editMessageText"
					if wire.MessageID != 55 {
						t.Fatalf("edit carrier = %d, want 55", wire.MessageID)
					}
				}
				if method != wantMethod || wire.Rich == nil || wire.Rich.Markdown != header+body.markdown || wire.Text != "" {
					t.Fatalf("wire = %s %+v; want %s %q", method, wire, wantMethod, header+body.markdown)
				}
			})
		}
	}
}
