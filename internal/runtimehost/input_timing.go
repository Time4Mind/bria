package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/processlog"
)

const slowInputThreshold = time.Second

type inputExecutionTiming struct {
	resolve              time.Duration
	download             time.Duration
	transcribe           time.Duration
	tmuxSend             time.Duration
	confirmationBaseline time.Duration
	confirmation         time.Duration
	inputBytes           int
	confirmationOutcome  string
	failureStage         string
}

type inputDeliveryTiming struct {
	ref                  string
	generation           uint64
	operationID          string
	kind                 string
	outcome              string
	total                time.Duration
	queue                time.Duration
	attachmentWait       time.Duration
	fifoWait             time.Duration
	resolve              time.Duration
	download             time.Duration
	transcribe           time.Duration
	prepare              time.Duration
	tmuxSend             time.Duration
	confirmationBaseline time.Duration
	confirmation         time.Duration
	inputBytes           int
	confirmationOutcome  string
}

func newInputDeliveryTiming(
	request Request,
	binding RuntimeBinding,
	queue inputQueueTiming,
	execution inputExecutionTiming,
	result Result,
	executionErr error,
	completedAt time.Time,
) inputDeliveryTiming {
	prepare := execution.resolve - execution.download - execution.transcribe
	prepare = nonNegativeDuration(prepare)
	total := nonNegativeDuration(completedAt.Sub(queue.enqueuedAt))
	if queue.enqueuedAt.IsZero() {
		total = queue.queue + execution.resolve + execution.confirmationBaseline +
			execution.tmuxSend + execution.confirmation
	}
	return inputDeliveryTiming{
		ref:                  fmt.Sprintf("%s/%s", binding.NodeID, binding.SessionID),
		generation:           request.ExpectedGeneration,
		operationID:          request.OperationID,
		kind:                 inputKindLabel(request),
		outcome:              inputOutcome(result, executionErr, execution.failureStage),
		total:                total,
		queue:                queue.queue,
		attachmentWait:       queue.attachmentWait,
		fifoWait:             queue.fifoWait,
		resolve:              execution.resolve,
		download:             execution.download,
		transcribe:           execution.transcribe,
		prepare:              prepare,
		tmuxSend:             execution.tmuxSend,
		confirmationBaseline: execution.confirmationBaseline,
		confirmation:         execution.confirmation,
		inputBytes:           execution.inputBytes,
		confirmationOutcome:  execution.confirmationOutcome,
	}
}

func inputKindLabel(request Request) string {
	if request.Input == nil {
		return "text"
	}
	switch request.Input.Kind {
	case InputVoice:
		return "voice"
	case InputPhoto:
		return "photo"
	case InputDocument:
		return "document"
	default:
		return "external"
	}
}

func inputOutcome(result Result, executionErr error, failureStage string) string {
	if errors.Is(executionErr, ErrInputUnconfirmed) || failureStage == "confirmation" {
		return "submit_unconfirmed"
	}
	if executionErr == nil && result.Delivered {
		return "delivered"
	}
	if errors.Is(executionErr, ErrStaleRuntime) {
		return "stale_generation"
	}
	if errors.Is(executionErr, ErrRuntimeShuttingDown) || errors.Is(executionErr, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(executionErr, ErrRuntimeUnavailable) {
		return "runtime_unavailable"
	}
	switch failureStage {
	case "resolve":
		return "resolve_failed"
	case "tmux_send":
		return "tmux_send_failed"
	default:
		return "failed"
	}
}

func logInputDeliveryTiming(timing inputDeliveryTiming) {
	outcome := timing.outcome
	format := "bria input_timing: stage=delivery ref=%q generation=%d operation=%q kind=%s outcome=%s total_ms=%d queue_ms=%d attachment_wait_ms=%d fifo_wait_ms=%d resolve_ms=%d download_ms=%d transcribe_ms=%d prepare_ms=%d tmux_send_ms=%d confirmation_baseline_ms=%d confirmation_ms=%d confirmation=%s input_bytes=%d slow_input=%t"
	args := []any{
		timing.ref, timing.generation, timing.operationID, timing.kind, outcome,
		durationMilliseconds(timing.total), durationMilliseconds(timing.queue),
		durationMilliseconds(timing.attachmentWait), durationMilliseconds(timing.fifoWait),
		durationMilliseconds(timing.resolve), durationMilliseconds(timing.download),
		durationMilliseconds(timing.transcribe), durationMilliseconds(timing.prepare),
		durationMilliseconds(timing.tmuxSend), durationMilliseconds(timing.confirmationBaseline),
		durationMilliseconds(timing.confirmation),
		timing.confirmationOutcome, timing.inputBytes, timing.total >= slowInputThreshold,
	}
	processlog.Outcomef(processlog.Detail, outcome, format, args...)
	if timing.total >= slowInputThreshold || outcome != "delivered" {
		processlog.Outcomef(processlog.Service, outcome, format, args...)
	}
}

func (e *LocalExecutor) emitInputTiming(timing inputDeliveryTiming) {
	if e.inputTiming != nil {
		e.inputTiming(timing)
	}
}

func durationMilliseconds(duration time.Duration) int64 {
	return nonNegativeDuration(duration).Milliseconds()
}

func nonNegativeDuration(duration time.Duration) time.Duration {
	if duration < 0 {
		return 0
	}
	return duration
}
