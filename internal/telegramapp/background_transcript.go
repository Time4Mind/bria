package telegramapp

import (
	"context"
	"errors"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

func (h *Handler) readBackgroundTranscript(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
) ([]transcript.Event, error) {
	requestCtx, cancel := context.WithTimeout(ctx, backgroundTranscriptBudget)
	defer cancel()
	startedAt := time.Now()
	events, err := h.controls.Transcript(requestCtx, actor, ref)
	duration := time.Since(startedAt)
	h.cardDataMu.Lock()
	h.transcriptReads++
	h.transcriptTotal += duration
	if duration > h.transcriptMax {
		h.transcriptMax = duration
	}
	if errors.Is(err, context.DeadlineExceeded) || requestCtx.Err() == context.DeadlineExceeded {
		h.transcriptSlow++
	}
	h.cardDataMu.Unlock()
	return events, err
}
