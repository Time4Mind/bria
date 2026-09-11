package nativeadapter

import (
	"context"
	"time"
	"unicode/utf8"

	"bria/internal/nativecapture"
	"bria/internal/nativecli"
	"bria/internal/runtimeprotocol"
)

type nativeObservationState struct {
	hash, model string
	interactive bool
	sentAt      time.Time
}

// Screen observations are transport UI state, never transcript events.
func (a *adapter) observeScreen(ctx context.Context, now time.Time) error {
	if !a.observation.sentAt.IsZero() && now.Sub(a.observation.sentAt) < 300*time.Millisecond {
		return nil
	}
	text, err := a.term.Capture(ctx)
	if err != nil {
		return err
	}
	return a.emitScreenObservation(text, now)
}

func (a *adapter) emitScreenObservation(text string, now time.Time) error {
	parsed := nativecli.ParseScreen(text)
	fullText := nativecapture.Bound(text, a.config.ScreenCaptureLimitKiB)
	hash := nativecapture.Hash(fullText)
	if parsed.Interactive {
		text = parsed.Content
	}
	if parsed.Model != "" {
		a.model = parsed.Model
	}
	text = nativecapture.Bound(text, a.config.ScreenCaptureLimitKiB)
	if len(text) > 0 {
		for !utf8.ValidString(text) {
			text = text[1:]
		}
	}
	previous := a.observation
	if previous.hash != "" && previous.model == a.model && previous.interactive == parsed.Interactive && previous.hash == hash {
		a.observation.sentAt = now
		return nil
	}
	if err := a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeNativeObservation, ProviderSessionID: a.id, Text: text, FullText: fullText, Hash: hash, Model: a.model, Interactive: parsed.Interactive}); err != nil {
		return err
	}
	a.observation = nativeObservationState{hash: hash, model: a.model, interactive: parsed.Interactive, sentAt: now}
	return nil
}
