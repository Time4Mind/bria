package telegramflow_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/storage"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramruntimecomposition"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

// Exercise the public send/commit boundary, then insert the next provider
// event into the committed history. A green send alone is not acceptance.
func TestCardCommitsKeepProviderOrderAndVisibleFinal(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprintf("queued_prompt=%t", queued), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "ui.json")
			open := func() *storage.SessionStore {
				t.Helper()
				store, err := storage.OpenSessionStore(path)
				if err != nil {
					t.Fatal(err)
				}
				return store
			}
			store := open()
			session, err := domain.NewStartingSession(flowSessionID, "intent", "local", domain.ProviderCodex, "/workspace")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.PutStartingIfAbsent(ctx, session); err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
				card := telegramstate.Card{SessionID: flowSessionID,
					Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99},
					Page:    telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}}
				return state.SetCard(card)
			}); err != nil {
				t.Fatal(err)
			}
			if err := store.SetCardPrompt(ctx, flowSessionID, "prompt-1", "SHOW PLAN"); err != nil {
				t.Fatal(err)
			}
			steps := []struct{ prompt, kind, text string }{
				{"prompt-1", "commentary", "I WILL PREPARE THE PLAN"},
				{"prompt-1", "tool", strings.Repeat("tool-one ", 40)},
				{"prompt-1", "tool", strings.Repeat("tool-two ", 40)},
				{"prompt-1", "tool", strings.Repeat("tool-three ", 40)},
				{"prompt-1", "tool", strings.Repeat("tool-four ", 40)},
				{"prompt-1", "final", "FINAL PLAN: inspect, compare, report."},
			}
			want := []string{"SHOW PLAN"}
			for _, step := range steps {
				want = append(want, step.text)
			}
			if queued {
				want = append(want, "NEXT REQUEST", "SECOND FINAL")
				steps = append(steps, struct{ prompt, kind, text string }{"prompt-2", "final", "SECOND FINAL"})
			}
			transport := &historyOrderTransport{sender: sender{receipt: coordinator.Receipt{MessageID: 99}}}
			for i, step := range steps {
				// Reopen between events, as after a restart; never rely on an
				// in-memory controller tail to recover lost durable turn anchors.
				store = open()
				if queued && i == 1 {
					// Enqueue after the previous commit, including an idempotent
					// prompt-status replacement through the real storage API.
					for repeat := 0; repeat < 2; repeat++ {
						if err := store.SetCardPrompt(ctx, flowSessionID, "prompt-2", "NEXT REQUEST"); err != nil {
							t.Fatalf("new prompt after a committed provider event: %v", err)
						}
					}
				}
				if err := store.InsertCardTypedHistoryAfterPrompt(ctx, flowSessionID, step.prompt, step.text, step.kind); err != nil {
					t.Fatal(err)
				}
				state, err := store.LoadTelegramUI(ctx)
				if err != nil {
					t.Fatal(err)
				}
				card, _ := state.Card(flowSessionID)
				blocks := make([]telegramui.ContentBlock, len(card.History))
				for j, text := range card.History {
					blocks[j] = telegramui.ContentBlock{Anchor: fmt.Sprintf("item-%d", j), Content: text}
				}
				pages, err := telegramui.PaginateContent(blocks, telegramui.PageLimits{MaxBytes: 512, MaxRunes: 512})
				if err != nil {
					t.Fatal(err)
				}
				view := telegramui.PageView{Page: len(pages.Pages), Pages: len(pages.Pages), FollowLatest: true}
				input := telegramui.CardProjectionInput{Pages: pages.Pages, View: view, Keyboard: telegramui.CardKeyboardInput{View: view}}
				commits := 2 // Repeated background refresh without a new event.
				if step.kind == "final" {
					commits = 1
				}
				for commit := 0; commit < commits; commit++ {
					now := time.Unix(1_800_000_000, 0).UTC()
					presenter := newPresenter(t, now)
					_, outbound, err := telegramflow.New(telegramflow.Config{
						OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
						CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }),
						UIState:          telegramruntimecomposition.SessionTelegramUIStore{State: store}, MessageUI: semanticMessageHandler{}, Callbacks: &globalCallbackExecutor{},
						Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: transport,
					})
					if err != nil {
						t.Fatal(err)
					}
					op := fmt.Sprintf("event-%d-commit-%d", i, commit)
					var prepared telegramflow.Prepared
					if step.kind == "final" {
						prepared, err = telegramflow.PrepareCompletion(op, flowSessionID, 42, true, "", input, false, nil, presenter)
					} else {
						prepared, err = telegramflow.PrepareCardRefresh(op, flowSessionID, 42, 99, input, "", false, nil, presenter)
					}
					if err != nil {
						t.Fatal(err)
					}
					if err := outbound.Register(prepared); err != nil {
						t.Fatal(err)
					}
					if step.kind == "final" {
						_, err = outbound.SendStatusWithKeyboard(ctx, op, prepared.Status, prepared.Keyboard)
					} else {
						_, err = outbound.EditStatusWithKeyboard(ctx, op, prepared.Status, prepared.Keyboard)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			state, err := open().LoadTelegramUI(ctx)
			if err != nil {
				t.Fatal(err)
			}
			card, _ := state.Card(flowSessionID)
			if !reflect.DeepEqual(card.History, want) {
				t.Errorf("committed history is out of provider order: got %q, want %q", card.History, want)
			}
			if !strings.Contains(transport.last, want[len(want)-1]) || strings.Contains(transport.last, steps[0].text) {
				t.Errorf("visible last page must show final, not earlier commentary: %q", transport.last)
			}
		})
	}
}

type historyOrderTransport struct {
	sender
	last string
}

func (s *historyOrderTransport) SendStatusWithKeyboard(ctx context.Context, id string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	s.last = status.Text
	return s.sender.SendStatusWithKeyboard(ctx, id, status, keyboard)
}
