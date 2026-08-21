package telegramapp

import (
	"context"
	"fmt"

	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const maxCardTransports = 32

// editCardTransportCoordinated is the single local writer for a Telegram card.
// Callers hold the actor's response-card lane. Replicated card metadata intentionally does not
// advance for every live pane frame, so this small process-local projection is
// what lets a background renderer rebase onto the pane worker's latest edit.
func (h *Handler) editCardTransportCoordinated(
	ctx context.Context,
	message telegrambot.Message,
	screen telegramui.Screen,
) (telegrambot.Message, error) {
	key := cardTransportKey(message)
	h.cardTransportMu.Lock()
	current, locallyKnown := h.cardTransports[key]
	h.cardTransportMu.Unlock()
	if locallyKnown {
		message = current
	}
	fingerprint := telegrambot.ScreenFingerprint(screen)
	if locallyKnown && message.ScreenHash != "" && message.ScreenHash == fingerprint {
		return message, nil
	}
	edited, err := h.messenger.EditScreen(ctx, message, screen)
	if err != nil {
		return edited, err
	}
	if edited.ChatID == 0 {
		edited.ChatID = message.ChatID
	}
	if edited.MessageID == 0 {
		edited.MessageID = message.MessageID
	}
	// Messenger implementations are expected to stamp this value, but keep the
	// coordinator authoritative as well (and make transport fakes behave like
	// the real Telegram client).
	edited.ScreenHash = fingerprint
	h.rememberCardTransport(edited)
	return edited, nil
}

func (h *Handler) rememberCardTransport(message telegrambot.Message) {
	if message.ChatID == 0 || message.MessageID == 0 {
		return
	}
	h.cardTransportMu.Lock()
	defer h.cardTransportMu.Unlock()
	if h.cardTransports == nil {
		h.cardTransports = make(map[string]telegrambot.Message)
	}
	key := cardTransportKey(message)
	for index, existing := range h.cardTransportOrder {
		if existing == key {
			h.cardTransportOrder = append(
				h.cardTransportOrder[:index], h.cardTransportOrder[index+1:]...,
			)
			break
		}
	}
	h.cardTransports[key] = message
	h.cardTransportOrder = append(h.cardTransportOrder, key)
	for len(h.cardTransportOrder) > maxCardTransports {
		oldest := h.cardTransportOrder[0]
		h.cardTransportOrder = h.cardTransportOrder[1:]
		delete(h.cardTransports, oldest)
	}
}

func cardTransportKey(message telegrambot.Message) string {
	return fmt.Sprintf("%d:%d", message.ChatID, message.MessageID)
}
