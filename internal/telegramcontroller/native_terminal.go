package telegramcontroller

import (
	"context"
	"errors"
	"strings"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramturnhelpers"
)

// Observe only the exact generation's runtime cache. Rendering must not query
// or time out a live CLI process, nor open an interactive overlay.
func (c *Controller) hydrateNativeModel(ctx context.Context, session domain.Session) {
	reader, ok := c.native.(sessionruntime.NativeModelProvider)
	if !ok || ctx.Err() != nil {
		return
	}
	model, known := reader.NativeModel(session.ID())
	if !known || strings.TrimSpace(model) == "" {
		model = ""
	}
	c.mu.Lock()
	current := c.nativeSnapshots[session.ID()]
	current.Model = model
	c.nativeSnapshots[session.ID()] = current
	c.mu.Unlock()
}

// Only Bria navigation commands are intercepted. The CLI owns every other
// command, its options, selected values and interactive screen.
func (c *Controller) isNativeInput(update coordinator.Update) bool {
	if update.Kind != coordinator.UpdateMessage || update.MediaKind != "" {
		return false
	}
	text := strings.TrimSpace(update.Text)
	if text == "" {
		return false
	}
	if strings.HasPrefix(text, "/") {
		switch text {
		case "/menu", "/sessions", "/status", "/stop":
			return false
		}
		if _, _, ok := telegramturnhelpers.ParseNew(text); ok {
			return false
		}
		if _, ok := telegramturnhelpers.ParseUse(text); ok {
			return false
		}
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.preprocessingInstructionPending && c.nativeOverlay == c.active && c.nativeSnapshots[c.active].Interactive
}

func (c *Controller) nativeCommand(ctx context.Context, id domain.SessionID, command string) string {
	if id == "" {
		return "Нет активной сессии для команды CLI."
	}
	if c.native == nil {
		return "Терминал CLI недоступен. Команда не отправлена модели."
	}
	snapshot, err := c.native.NativeControl(ctx, id, sessionruntime.NativeRequest{Command: command})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrNativeUnavailable) && strings.TrimSpace(snapshot.Text) != "" {
			return "Команда CLI недоступна.\n\n" + snapshot.Text
		}
		return "Не удалось передать ввод в терминал CLI. Запрос модели не отправлен."
	}
	c.mu.Lock()
	c.nativeSnapshots[id] = snapshot
	if snapshot.Interactive {
		c.nativeOverlay = id
	} else {
		c.nativeOverlay = ""
	}
	c.mu.Unlock()
	return snapshot.Text
}

func (c *Controller) nativeMessageSurface(text string) SemanticActionResult {
	c.mu.Lock()
	id := c.active
	snapshot := c.nativeSnapshots[id]
	c.mu.Unlock()
	if snapshot.Text != text {
		return c.modelNotice(id, text)
	}
	return nativeSurface(id, snapshot)
}

func nativeSurface(id domain.SessionID, snapshot sessionruntime.NativeSnapshot) SemanticActionResult {
	button := func(label string, choice int) SemanticButton {
		return SemanticButton{Label: label, Action: SemanticNativeKey, SessionID: id, Choice: choice}
	}
	text := snapshot.Text
	if text == "" {
		text = "CLI: ожидание вывода."
	}
	if !snapshot.Interactive {
		return SemanticActionResult{Surface: &SemanticSurface{Text: text, Rows: [][]SemanticButton{{{Label: "Меню", Action: SemanticMenuBack}, {Label: "К сессии", Action: SemanticSelect, SessionID: id}}}}}
	}
	rows := [][]SemanticButton{
		{button("␣ Space", 8), button("↑", 1), button("⇥ Tab", 7)},
		{button("←", 3), button("↓", 2), button("→", 4)},
		{button("⎋ Esc", 6), button("⏎ Enter", 5)},
		{{Label: "Меню", Action: SemanticMenuBack}, {Label: "К сессии", Action: SemanticSelect, SessionID: id}},
	}
	return SemanticActionResult{Surface: &SemanticSurface{Text: text, Rows: rows, NativeSessionID: id}}
}

// Projection observes the displayed overlay without reopening one behind a
// menu. Explicit user navigation alone may restore an interactive screen.
func (c *Controller) currentNativeSurface(id domain.SessionID) (SemanticActionResult, bool) {
	c.mu.Lock()
	snapshot, ok := c.nativeSnapshots[id]
	shown := c.nativeOverlay == id
	c.mu.Unlock()
	if !ok || !shown {
		return SemanticActionResult{}, false
	}
	return nativeSurface(id, snapshot), true
}

func (c *Controller) restoreNativeSurface(id domain.SessionID) (SemanticActionResult, bool) {
	c.mu.Lock()
	snapshot, ok := c.nativeSnapshots[id]
	// Selecting the displayed native card's "К сессии" is Back, not a
	// request to immediately reopen the same overlay. Selecting another
	// session first clears this displayed identity, so returning restores it.
	back := c.nativeOverlay == id
	if !back && ok && snapshot.Interactive {
		c.nativeOverlay = id
	} else {
		c.nativeOverlay = ""
	}
	c.mu.Unlock()
	if back || !ok || !snapshot.Interactive {
		return SemanticActionResult{}, false
	}
	return nativeSurface(id, snapshot), true
}

func (c *Controller) nativeKey(ctx context.Context, action SemanticAction) (SemanticActionResult, error) {
	c.mu.Lock()
	id := c.active
	snapshot, ok := c.nativeSnapshots[action.SessionID]
	shown := c.nativeOverlay == id
	c.mu.Unlock()
	if id != action.SessionID || !ok || !shown || !snapshot.Interactive || snapshot.Hash == "" || c.native == nil {
		return c.modelNotice(id, "Терминальная клавиатура устарела. Открой команду в текущей сессии заново."), nil
	}
	keys := [...]string{"up", "down", "left", "right", "enter", "escape", "tab", "space"}
	next, err := c.native.NativeControl(ctx, id, sessionruntime.NativeRequest{Key: keys[action.Choice-1], ExpectedHash: snapshot.Hash})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrNativeStale) && next.Hash != "" {
			return c.nativeKeyResult(ctx, id, next)
		}
		if errors.Is(err, sessionruntime.ErrNativeUnavailable) && strings.TrimSpace(next.Text) != "" {
			return c.modelNotice(id, "Клавиша CLI недоступна.\n\n"+next.Text), nil
		}
		return c.modelNotice(id, "Экран CLI изменился или недоступен. Клавиша не применена; открой команду заново."), nil
	}
	return c.nativeKeyResult(ctx, id, next)
}

func (c *Controller) nativeKeyResult(ctx context.Context, id domain.SessionID, snapshot sessionruntime.NativeSnapshot) (SemanticActionResult, error) {
	c.mu.Lock()
	c.nativeSnapshots[id] = snapshot
	if snapshot.Interactive {
		c.nativeOverlay = id
	} else {
		c.nativeOverlay = ""
	}
	c.mu.Unlock()
	if snapshot.Interactive {
		return nativeSurface(id, snapshot), nil
	}
	card, err := c.semanticCard(ctx, id, true)
	return SemanticActionResult{Card: &card}, err
}
