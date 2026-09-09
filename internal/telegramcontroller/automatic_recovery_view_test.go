package telegramcontroller_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

func TestAutomaticRecoveryCardDoesNotDemandOwnerIntervention(t *testing.T) {
	ctx := context.Background()
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "same-provider-session", 1)
	running, err := ready.StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err := running.AwaitRecoveryAt(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store := newLockedSessions(awaiting)
	c := newController(t, nil, store, nil, nil, telegramcontroller.Options{
		Recoverer: deferredRecoverer(func(context.Context, domain.SessionID) (domain.Session, error) { return awaiting, nil }),
	})
	t.Cleanup(func() { _ = c.Close(ctx) })
	result, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: awaiting.ID()})
	if err != nil || result.Card == nil {
		t.Fatalf("history card=%+v err=%v", result, err)
	}
	for _, text := range []string{"Требуется восстановление", "ввод отключён", "Исход предыдущего запроса", "Выбери восстановление"} {
		if strings.Contains(result.Card.Header, text) {
			t.Errorf("automatic recovery demands manual action: %s", result.Card.Header)
		}
	}
	if result.Card.SessionID != awaiting.ID() || !result.Card.Recovery || len(result.Card.Pages) == 0 {
		t.Fatal("automatic recovery must preserve the real state and readable history")
	}
}
