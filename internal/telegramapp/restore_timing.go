package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
)

type restoreTimingContextKey struct{}

type restoreTimingTag struct {
	ref        domain.SessionRef
	generation uint64
	stage      string
}

func withRestoreTiming(
	ctx context.Context,
	ref domain.SessionRef,
	generation uint64,
	stage string,
) context.Context {
	return context.WithValue(ctx, restoreTimingContextKey{}, restoreTimingTag{
		ref: ref, generation: generation, stage: stage,
	})
}

func restoreTimingFromContext(ctx context.Context) (restoreTimingTag, bool) {
	if ctx == nil {
		return restoreTimingTag{}, false
	}
	tag, ok := ctx.Value(restoreTimingContextKey{}).(restoreTimingTag)
	return tag, ok && tag.ref.Validate() == nil && tag.generation > 0 && tag.stage != ""
}

func restoreSettlementOutcome(session domain.Session, expectedGeneration uint64) (bool, string) {
	if expectedGeneration > 0 && session.RuntimeGeneration != expectedGeneration {
		return true, "superseded"
	}
	if session.ResumePending {
		return false, "waiting"
	}
	if !session.IsLive() && session.ArchiveReason == domain.ArchiveResumeFailed {
		return true, "resume_failed"
	}
	if !session.IsLive() {
		return true, "not_live"
	}
	return true, "ready"
}

type restoreCallbackTiming struct {
	startedAt  time.Time
	ref        domain.SessionRef
	generation uint64
	outcome    string
	resolve    time.Duration
	ack        time.Duration
	control    time.Duration
	render     time.Duration
	edit       time.Duration
}

func newRestoreCallbackTiming() *restoreCallbackTiming {
	return &restoreCallbackTiming{startedAt: time.Now(), outcome: "not_completed"}
}

func (timing *restoreCallbackTiming) log() {
	if timing == nil {
		return
	}
	total := time.Since(timing.startedAt)
	format := "bria restore_timing: stage=callback ref=%q generation=%d outcome=%s total_ms=%d resolve_ms=%d callback_ack_ms=%d control_ms=%d initial_render_ms=%d initial_edit_ms=%d slow_restore=%t"
	arguments := []any{
		timing.ref.Key(), timing.generation, timing.outcome, total.Milliseconds(),
		timing.resolve.Milliseconds(), timing.ack.Milliseconds(),
		timing.control.Milliseconds(), timing.render.Milliseconds(), timing.edit.Milliseconds(),
		total > time.Second,
	}
	processlog.Detailf(format, arguments...)
	if total > time.Second {
		processlog.Servicef(format, arguments...)
	}
}

type restoreReadyTiming struct {
	startedAt          time.Time
	waitStartedAt      time.Time
	ref                domain.SessionRef
	generation         uint64
	observedGeneration uint64
	outcome            string
	wait               time.Duration
	render             time.Duration
	edit               time.Duration
}

func (timing *restoreReadyTiming) log() {
	if timing == nil {
		return
	}
	total := time.Since(timing.startedAt)
	format := "bria restore_timing: stage=ready ref=%q generation=%d observed_generation=%d outcome=%s total_ms=%d settle_wait_ms=%d settled_render_ms=%d settled_edit_ms=%d slow_restore=%t"
	arguments := []any{
		timing.ref.Key(), timing.generation, timing.observedGeneration, timing.outcome, total.Milliseconds(),
		timing.wait.Milliseconds(), timing.render.Milliseconds(), timing.edit.Milliseconds(),
		total > time.Second,
	}
	processlog.Detailf(format, arguments...)
	if total > time.Second {
		processlog.Servicef(format, arguments...)
	}
}
