package telegramoutbound

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const slowTelegramOperation = 750 * time.Millisecond
const tracedTelegramOperation = 25 * time.Millisecond

type restoreTraceContextKey struct{}

// RestoreTrace carries bounded, content-free restore correlation.
type RestoreTrace struct {
	Ref        string
	Generation uint64
	Stage      string
}

// WithRestoreTrace associates outbound timing with a restore flow. Invalid or
// incomplete traces are ignored so transport behavior never depends on diagnostics.
func WithRestoreTrace(ctx context.Context, trace RestoreTrace) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if trace.Ref == "" || trace.Generation == 0 || trace.Stage == "" {
		return ctx
	}
	return context.WithValue(ctx, restoreTraceContextKey{}, trace)
}

func restoreTraceFromContext(ctx context.Context) (RestoreTrace, bool) {
	if ctx == nil {
		return RestoreTrace{}, false
	}
	trace, ok := ctx.Value(restoreTraceContextKey{}).(RestoreTrace)
	return trace, ok && trace.Ref != "" && trace.Generation > 0 && trace.Stage != ""
}

// LogOperation records an outbound-adjacent queue or transport phase using
// the same bounded schema as Coordinator operations.
func LogOperation(
	ctx context.Context,
	operation string,
	messageID int64,
	startedAt time.Time,
	err error,
) {
	logSlowTelegramOperationContext(ctx, operation, messageID, startedAt, err)
}

func screenOperation(base string, screen telegramui.Screen) string {
	if screen.Pane == nil {
		return base + "_text"
	}
	if len(screen.Pane.PNG) > 0 {
		return base + "_upload"
	}
	if screen.Pane.FileID != "" {
		return base + "_file_id"
	}
	return base + "_pane"
}

func logSlowTelegramOperation(
	operation string,
	messageID int64,
	startedAt time.Time,
	err error,
) {
	logSlowTelegramOperationContext(
		context.Background(), operation, messageID, startedAt, err,
	)
}

func logSlowTelegramOperationContext(
	ctx context.Context,
	operation string,
	messageID int64,
	startedAt time.Time,
	err error,
) {
	duration := time.Since(startedAt)
	if duration < tracedTelegramOperation {
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	traceFields := ""
	if trace, ok := restoreTraceFromContext(ctx); ok {
		traceFields = " restore_ref=" + strconv.Quote(trace.Ref) + " restore_generation=" +
			strconv.FormatUint(trace.Generation, 10) + " restore_stage=" + trace.Stage
	}
	processlog.Failuref(
		processlog.Detail, FailureClass(err),
		"bria telegram: outbound_timing operation=%s message_id=%d duration_ms=%d outcome=%s%s",
		operation, messageID, duration.Milliseconds(), outcome, traceFields,
	)
	if duration < slowTelegramOperation {
		return
	}
	processlog.Failuref(
		processlog.Service, FailureClass(err),
		"bria telegram: slow_outbound operation=%s message_id=%d duration_ms=%d outcome=%s%s",
		operation, messageID, duration.Milliseconds(), outcome, traceFields,
	)
}

// FailureClass maps an outbound transport error to the shared bounded
// process-log taxonomy without rendering error text.
func FailureClass(err error) processlog.FailureClass {
	switch {
	case err == nil:
		return processlog.FailureNone
	case errors.Is(err, context.DeadlineExceeded):
		return processlog.FailureTimeout
	case errors.Is(err, context.Canceled):
		return processlog.FailureCancelled
	}
	if _, limited := telegrambot.FloodWait(err); limited {
		return processlog.FailureRateLimited
	}
	return processlog.FailureTransport
}
