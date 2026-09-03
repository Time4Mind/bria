// Package telegramcards renders the semantic session view into bounded card
// pages. Telegram transport and callback encoding remain outside this package.
package telegramcards

import (
	"errors"
	"fmt"
	"strings"

	"bria/internal/telegramui"
)

// Input is the complete semantic input for one session card. History content
// is already copy-neutral; this package only adds the stable session header.
type Input struct {
	Card            telegramui.SessionCard
	History         []telegramui.ContentBlock
	View            telegramui.PageView
	OptionsExpanded bool
	SessionRowSizes []int
	Limits          telegramui.PageLimits
}

// Card is ready for a transport presenter: one selected page and its semantic
// keyboard, while Pages retains the complete history for later navigation.
type Card struct {
	Pages    []telegramui.ContentPage
	View     telegramui.PageView
	Keyboard telegramui.CardKeyboard
}

// Render creates a session card. With no remembered view it follows the
// latest page, which keeps an active card showing new events by default.
func Render(input Input) (Card, error) {
	if err := validateCard(input.Card); err != nil {
		return Card{}, err
	}
	header := sessionHeader(input.Card)
	for _, block := range input.History {
		if block.Anchor == "session-header" {
			return Card{}, errors.New("history anchor conflicts with session header")
		}
	}
	limits := input.Limits
	if limits.MaxRunes == 0 {
		limits.MaxRunes = 4096
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = 16384
	}
	// Repeat the compact session header on every page. This keeps a paginated
	// Telegram card understandable when a user opens a later page directly.
	headerLimits := limits
	headerLimits.MaxRunes -= runeCount(header)
	headerLimits.MaxBytes -= len(header)
	if len(input.History) == 0 {
		if headerLimits.MaxRunes < 1 || headerLimits.MaxBytes < 1 {
			return Card{}, errors.New("page limits cannot contain session header")
		}
		pagination, err := telegramui.PaginateContent([]telegramui.ContentBlock{{Anchor: "session-header", Content: header}}, limits)
		if err != nil {
			return Card{}, fmt.Errorf("paginate session card: %w", err)
		}
		return buildCard(input, pagination.Pages)
	}
	if headerLimits.MaxRunes < 1 || headerLimits.MaxBytes < 1 {
		return Card{}, errors.New("page limits cannot contain session header and history")
	}
	pagination, err := telegramui.PaginateContent(input.History, headerLimits)
	if err != nil {
		return Card{}, fmt.Errorf("paginate session card: %w", err)
	}
	for index := range pagination.Pages {
		pagination.Pages[index].Content = header + pagination.Pages[index].Content
	}
	return buildCard(input, pagination.Pages)
}

func buildCard(input Input, pages []telegramui.ContentPage) (Card, error) {
	view := input.View
	if view.Page == 0 && view.Pages == 0 {
		view = telegramui.PageView{Page: 1, Pages: len(pages), FollowLatest: true}
	}
	view, err := telegramui.ReflowPageView(view, pages)
	if err != nil {
		return Card{}, fmt.Errorf("resolve session card page: %w", err)
	}
	keyboardInput := telegramui.CardKeyboardInput{
		View:            view,
		Working:         isWorkingState(input.Card.State),
		Archived:        isArchivedState(input.Card.State),
		OptionsExpanded: input.OptionsExpanded,
		SessionRowSizes: append([]int(nil), input.SessionRowSizes...),
	}
	keyboard, err := telegramui.ProjectCardKeyboard(keyboardInput)
	if err != nil {
		return Card{}, fmt.Errorf("project session card keyboard: %w", err)
	}
	return Card{Pages: pages, View: view, Keyboard: keyboard}, nil
}

func isArchivedState(state telegramui.SessionState) bool {
	return state == telegramui.SessionArchived || state == telegramui.SessionResumeFailed
}

func isWorkingState(state telegramui.SessionState) bool {
	switch state {
	case telegramui.SessionRunning, telegramui.SessionStopping, telegramui.SessionClosingAfterWork:
		return true
	default:
		return false
	}
}

func runeCount(value string) int { return len([]rune(value)) }

func validateCard(card telegramui.SessionCard) error {
	if strings.TrimSpace(string(card.Computer)) == "" {
		return errors.New("session card computer is required")
	}
	if card.Provider != "codex" && card.Provider != "claude" {
		return fmt.Errorf("session card provider %q is unsupported", card.Provider)
	}
	if strings.TrimSpace(card.Workdir) == "" {
		return errors.New("session card workdir is required")
	}
	switch card.State {
	case telegramui.SessionStarting,
		telegramui.SessionResuming,
		telegramui.SessionReady,
		telegramui.SessionRunning,
		telegramui.SessionStopping,
		telegramui.SessionClosingAfterWork,
		telegramui.SessionAwaitingRecovery,
		telegramui.SessionClosing,
		telegramui.SessionArchived,
		telegramui.SessionResumeFailed:
		return nil
	default:
		return fmt.Errorf("session card state %q is unsupported", card.State)
	}
}

func sessionHeader(card telegramui.SessionCard) string {
	return "Сессия\n" +
		"Компьютер: " + string(card.Computer) + "\n" +
		"Исполнитель: " + string(card.Provider) + "\n" +
		"Рабочая папка: " + card.Workdir + "\n" +
		"Статус: " + stateCopy(card.State) + "\n\n"
}

func stateCopy(state telegramui.SessionState) string {
	switch state {
	case telegramui.SessionStarting:
		return "запуск"
	case telegramui.SessionResuming:
		return "продолжается"
	case telegramui.SessionReady:
		return "готова"
	case telegramui.SessionRunning:
		return "работает"
	case telegramui.SessionStopping:
		return "останавливается"
	case telegramui.SessionClosingAfterWork:
		return "закрытие после работы"
	case telegramui.SessionAwaitingRecovery:
		return "ожидает восстановления"
	case telegramui.SessionClosing:
		return "закрывается"
	case telegramui.SessionArchived:
		return "в архиве"
	case telegramui.SessionResumeFailed:
		return "ошибка продолжения"
	default:
		return string(state)
	}
}
