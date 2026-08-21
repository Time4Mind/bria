package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegrambot"
)

func (h *Handler) refreshRestoredCard(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	origin telegrambot.Message,
	startedAt time.Time,
	waitStartedAt time.Time,
	generation uint64,
) {
	timing := &restoreReadyTiming{
		startedAt: startedAt, waitStartedAt: waitStartedAt,
		ref: ref, generation: generation, outcome: "waiting",
	}
	defer timing.log()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(35 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			timing.outcome = "cancelled"
			return
		case <-timer.C:
			timing.outcome = "timeout"
			return
		case <-ticker.C:
			if !h.canRefresh() {
				timing.outcome = "leadership_lost"
				return
			}
			session, err := h.service.Session(actor, ref)
			if err != nil {
				continue
			}
			timing.observedGeneration = session.RuntimeGeneration
			settled, settledOutcome := restoreSettlementOutcome(session, generation)
			if !settled {
				continue
			}
			if settledOutcome == "superseded" {
				timing.outcome = settledOutcome
				return
			}
			timing.wait = time.Since(waitStartedAt)
			settledCtx := withRestoreTiming(ctx, ref, session.RuntimeGeneration, "settled")
			phaseStartedAt := time.Now()
			screen, err := h.renderSessionCard(settledCtx, actor, ref, 0)
			timing.render = time.Since(phaseStartedAt)
			if err == nil {
				phaseStartedAt = time.Now()
				if edited, editErr := h.editExplicitSessionScreen(
					settledCtx, actor, origin, screen,
				); editErr == nil {
					timing.edit = time.Since(phaseStartedAt)
					timing.outcome = settledOutcome
					h.schedulePaneRefresh(ctx, actor, ref, edited)
				} else {
					timing.edit = time.Since(phaseStartedAt)
					timing.outcome = "settled_edit_failed"
				}
			} else {
				timing.outcome = "settled_render_failed"
			}
			return
		}
	}
}

func (h *Handler) editUnavailableSession(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	origin telegrambot.Message,
) error {
	screen, err := h.renderSessionCard(ctx, actor, ref, 0)
	if err != nil {
		return nil
	}
	screen.Text += "\n\n" + h.copy(actor).Text(i18n.ToastUnavailable)
	var edited telegrambot.Message
	edited, err = h.editExplicitSessionScreen(ctx, actor, origin, screen)
	if err == nil {
		h.schedulePaneRefresh(ctx, actor, ref, edited)
	}
	return err
}

func (h *Handler) refreshSettledCard(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	operationID string,
	origin telegrambot.Message,
) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
			if !h.canRefresh() {
				return
			}
			session, err := h.service.Session(actor, ref)
			if err != nil || session.LastOperation == nil ||
				session.LastOperation.OperationID != operationID ||
				session.LastOperation.Status == domain.OperationQueued {
				continue
			}
			screen, err := h.renderSessionCard(ctx, actor, ref, 0)
			if err == nil {
				if edited, editErr := h.editExplicitSessionScreen(
					ctx, actor, origin, screen,
				); editErr == nil {
					h.schedulePaneRefresh(ctx, actor, ref, edited)
				}
			}
			return
		}
	}
}
