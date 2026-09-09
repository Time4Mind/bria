package telegramcompletioncomposition

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramstate"
)

func TestOnlyFinalMarksPreparedPublicationOperation(t *testing.T) {
	for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationFinal, telegramcontroller.NotificationError, telegramcontroller.NotificationQuestion, telegramcontroller.NotificationCommentary} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			const id domain.SessionID = "11111111-1111-4111-9111-111111111111"
			codec, err := callbacktoken.New(bytes.Repeat([]byte{7}, 32), nil, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			presenter, err := telegrambridge.NewPresenter(codec, time.Now, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			store := telegramstate.NewMemoryStore()
			if err := store.Update(ctx, func(s *telegramstate.State) error {
				s.ActiveSession = id
				return s.SetCard(telegramstate.Card{SessionID: id, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 55}, Page: telegramstate.Page{Current: 1, Total: 1}})
			}); err != nil {
				t.Fatal(err)
			}
			card := telegramcontroller.SemanticCard{SessionID: id, Header: "Session", Pages: []telegramcontroller.SemanticContentPage{{Content: "Final", Anchors: []string{"final"}, FinalStart: true}}, View: telegramcontroller.SemanticPageView{Page: 1, Pages: 1}}
			sender := &completionSenderStub{}
			d := CompletionDeliverer{Controller: completionControllerStub{card: card, active: true}, Cards: store, Sender: sender, Presenter: presenter, ConversationID: 42}
			receipt, err := d.Deliver(ctx, telegramcontroller.Notification{SessionID: id, Kind: kind, OperationID: "request:final"}, "request:final")
			if err != nil || receipt.State != "confirmed" {
				t.Fatalf("delivery=%+v err=%v", receipt, err)
			}
			want := ""
			if kind == telegramcontroller.NotificationFinal {
				want = "request:final"
			}
			if sender.prepared.Card.FinalOperationID != want {
				t.Fatalf("final commit identity=%q want=%q", sender.prepared.Card.FinalOperationID, want)
			}
			if kind == telegramcontroller.NotificationFinal && sender.prepared.Edit {
				t.Fatal("final must use a new carrier")
			}
			if kind == telegramcontroller.NotificationFinal {
				if err := store.Update(ctx, func(s *telegramstate.State) error {
					stored, _ := s.Card(id)
					stored.PendingFinalOperations = []string{"request-a:final", "request-b:final"}
					return s.SetCard(stored)
				}); err != nil {
					t.Fatal(err)
				}
				card.Pages = []telegramcontroller.SemanticContentPage{
					{Content: "A_BEGIN", Anchors: []string{"a"}, FinalStart: true, FinalOperationID: "request-a:final"},
					{Content: "A_END", Anchors: []string{"a-end"}, FinalOperationID: "request-a:final"},
					{Content: "B_BEGIN", Anchors: []string{"b"}, FinalStart: true, FinalOperationID: "request-b:final"},
				}
				card.View = telegramcontroller.SemanticPageView{Page: 3, Pages: 3, FollowLatest: true}
				d.Controller = completionControllerStub{card: card, active: true}
				for _, op := range []string{"request-a:final", "request-b:final"} {
					sender.prepared.OperationID = ""
					if _, err := d.Deliver(ctx, telegramcontroller.Notification{SessionID: id, Kind: kind}, op); err != nil {
						t.Fatal(err)
					}
					wantText := "A_BEGIN"
					if op == "request-b:final" {
						wantText = "B_BEGIN"
					}
					if !strings.Contains(sender.status.Text, wantText) || sender.prepared.Card.FinalOperationID != op {
						t.Fatalf("wrong exact final selected for %s", op)
					}
				}
				sender.prepared.OperationID = ""
				if _, err := d.Deliver(ctx, telegramcontroller.Notification{SessionID: id, Kind: kind}, "missing:final"); err == nil || sender.prepared.OperationID != "" {
					t.Fatal("known final metadata fell back to a different answer")
				}
				for i := range card.Pages {
					card.Pages[i].FinalOperationID = ""
				}
				d.Controller = completionControllerStub{card: card, active: true}
				if _, err := d.Deliver(ctx, telegramcontroller.Notification{SessionID: id, Kind: kind}, "request-a:final"); err == nil {
					t.Fatal("durable pending final permitted an unbound fallback")
				}
			}
		})
	}
}
