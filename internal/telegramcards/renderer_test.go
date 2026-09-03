package telegramcards_test

import (
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramcards"
	"bria/internal/telegramui"
)

func TestRenderSessionCardBuildsHistoryPagesAndSemanticKeyboard(t *testing.T) {
	card := mustSession(t, domain.ProviderCodex, domain.SessionReady)
	got, err := telegramcards.Render(telegramcards.Input{
		Card: card,
		History: []telegramui.ContentBlock{
			{Anchor: "user-1", Content: "Проверь проект"},
			{Anchor: "final-1", Content: "Готово. " + strings.Repeat("итог ", 30)},
		},
		OptionsExpanded: true,
		SessionRowSizes: []int{2},
		Limits:          telegramui.PageLimits{MaxRunes: 180, MaxBytes: 720},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(got.Pages) < 1 || got.View.Page != got.View.Pages {
		t.Fatalf("Render() view = %#v, pages = %#v; want latest page", got.View, got.Pages)
	}
	if !strings.Contains(strings.Join(pageTexts(got.Pages), ""), "Исполнитель: codex") {
		t.Fatalf("rendered pages do not contain session header: %#v", got.Pages)
	}
	if !strings.Contains(strings.Join(pageTexts(got.Pages), ""), "Проверь проект") ||
		!strings.Contains(strings.Join(pageTexts(got.Pages), ""), "Готово") {
		t.Fatalf("rendered pages lost history: %#v", got.Pages)
	}
	if got.Keyboard.Rows[1][0].Action != telegramui.ActionClose ||
		got.Keyboard.Rows[1][1].Action != telegramui.ActionOptions {
		t.Fatalf("semantic control row = %#v", got.Keyboard.Rows[1])
	}
	if len(got.Keyboard.Rows) != 4 || got.Keyboard.Rows[3][0].Target.SessionSlot != 1 {
		t.Fatalf("semantic keyboard = %#v", got.Keyboard)
	}
}

func TestRenderRejectsInvalidHistoryAndUsesStateCopy(t *testing.T) {
	card := mustSession(t, domain.ProviderClaude, domain.SessionAwaitingRecovery)
	_, err := telegramcards.Render(telegramcards.Input{
		Card:    card,
		History: []telegramui.ContentBlock{{Anchor: "", Content: "bad"}},
	})
	if err == nil {
		t.Fatal("Render() error = nil, want invalid history error")
	}

	got, err := telegramcards.Render(telegramcards.Input{
		Card:    card,
		History: []telegramui.ContentBlock{{Anchor: "event", Content: "ожидает"}},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(got.Pages[0].Content, "Статус: ожидает восстановления") {
		t.Fatalf("state copy missing: %q", got.Pages[0].Content)
	}
}

func TestRenderSupportsEveryLifecycleState(t *testing.T) {
	states := map[telegramui.SessionState]string{
		telegramui.SessionStarting:         "запуск",
		telegramui.SessionResuming:         "продолжается",
		telegramui.SessionReady:            "готова",
		telegramui.SessionRunning:          "работает",
		telegramui.SessionStopping:         "останавливается",
		telegramui.SessionClosingAfterWork: "закрытие после работы",
		telegramui.SessionAwaitingRecovery: "ожидает восстановления",
		telegramui.SessionClosing:          "закрывается",
		telegramui.SessionArchived:         "в архиве",
		telegramui.SessionResumeFailed:     "ошибка продолжения",
	}
	for state, copy := range states {
		t.Run(string(state), func(t *testing.T) {
			card := telegramui.SessionCard{Computer: "mac", Provider: domain.ProviderCodex, Workdir: "/work", State: state}
			got, err := telegramcards.Render(telegramcards.Input{Card: card})
			if err != nil {
				t.Fatalf("Render(%s) error = %v", state, err)
			}
			if !strings.Contains(got.Pages[0].Content, "Статус: "+copy) {
				t.Fatalf("Render(%s) content = %q", state, got.Pages[0].Content)
			}
		})
	}
}

func TestRenderDerivesStopAndCloseFromLifecycle(t *testing.T) {
	running := telegramui.SessionCard{Computer: "mac", Provider: domain.ProviderCodex, Workdir: "/work", State: telegramui.SessionRunning}
	got, err := telegramcards.Render(telegramcards.Input{Card: running})
	if err != nil {
		t.Fatal(err)
	}
	if got.Keyboard.Rows[1][0].Action != telegramui.ActionStop {
		t.Fatalf("running lifecycle action = %q, want stop", got.Keyboard.Rows[1][0].Action)
	}
	ready := running
	ready.State = telegramui.SessionReady
	got, err = telegramcards.Render(telegramcards.Input{Card: ready})
	if err != nil {
		t.Fatal(err)
	}
	if got.Keyboard.Rows[1][0].Action != telegramui.ActionClose {
		t.Fatalf("ready lifecycle action = %q, want close", got.Keyboard.Rows[1][0].Action)
	}
}

func TestRenderArchivedCardOffersResume(t *testing.T) {
	archived := telegramui.SessionCard{Computer: "mac", Provider: domain.ProviderCodex, Workdir: "/work", State: telegramui.SessionArchived}
	got, err := telegramcards.Render(telegramcards.Input{Card: archived})
	if err != nil {
		t.Fatal(err)
	}
	if got.Keyboard.Rows[1][0].Action != telegramui.ActionResume {
		t.Fatalf("archived lifecycle action = %q, want resume", got.Keyboard.Rows[1][0].Action)
	}
}

func mustSession(t *testing.T, provider domain.Provider, status domain.SessionStatus) telegramui.SessionCard {
	t.Helper()
	session, err := domain.NewStartingSession("11111111-1111-4111-8111-111111111111", "intent-1", "laptop", provider, "/work")
	if err != nil {
		t.Fatal(err)
	}
	if status == domain.SessionReady {
		session, err = session.Ready(domain.ProviderBinding{Provider: provider, SessionID: "provider", Generation: 1})
	} else if status == domain.SessionAwaitingRecovery {
		session, err = session.AwaitRecovery()
	}
	if err != nil {
		t.Fatal(err)
	}
	return telegramui.ProjectSessionCard(session)
}

func pageTexts(pages []telegramui.ContentPage) []string {
	texts := make([]string, len(pages))
	for i := range pages {
		texts[i] = pages[i].Content
	}
	return texts
}
