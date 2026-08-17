package telegramapp

import (
	"context"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

const inputBaselineTimeout = 500 * time.Millisecond

type inputPendingBaseline struct {
	ref    domain.SessionRef
	events []transcript.Event
	known  bool
}

func pendingInputText(update telegrambot.IncomingUpdate) string {
	if update.Content.Kind == telegrambot.IncomingVoice {
		return ""
	}
	return strings.TrimSpace(update.Text)
}

// captureInputBaseline prefers the in-memory transcript that produced the
// currently visible card. It therefore preserves exactly what the user saw
// without a node round trip. A bounded transcript read covers process restarts
// where Telegram still has a card but the local rendering cache is cold.
func (h *Handler) captureInputBaseline(
	ctx context.Context,
	actor application.Principal,
) inputPendingBaseline {
	session, err := h.service.ActiveSession(actor)
	if err != nil || h.controls == nil {
		return inputPendingBaseline{}
	}
	baseline := inputPendingBaseline{ref: session.Ref()}
	if events, ok := h.cachedCardTranscript(session.Ref()); ok {
		baseline.events = events
		baseline.known = true
		return baseline
	}
	baselineCtx, cancel := context.WithTimeout(ctx, inputBaselineTimeout)
	defer cancel()
	events, err := h.controls.Transcript(baselineCtx, actor, session.Ref())
	if err != nil {
		return baseline
	}
	baseline.events = events
	baseline.known = true
	return baseline
}

func (h *Handler) pendingInputCard(
	actor application.Principal,
	ref domain.SessionRef,
	baseline inputPendingBaseline,
) (telegramui.Screen, error) {
	if _, err := h.service.Session(actor, ref); err != nil {
		return telegramui.Screen{}, err
	}
	events := []transcript.Event(nil)
	if baseline.known && baseline.ref == ref {
		events = baseline.events
	} else if cached, ok := h.cachedCardTranscript(ref); ok {
		events = cached
	}
	return h.projector.SessionCardPageWithContext(
		actor, ref, cardEvents(events), 0, h.cardContext(ref),
	)
}
