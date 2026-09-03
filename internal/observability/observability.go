// Package observability records terminal timing events without accepting raw
// messages, Telegram identifiers, paths, or other unstructured content.
package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"bria/internal/safelog"
)

var (
	ErrInvalidLogger        = errors.New("observability: invalid logger")
	ErrInvalidScope         = errors.New("observability: invalid high-entropy scope")
	ErrInvalidOperation     = errors.New("observability: invalid operation")
	ErrInvalidMeasurements  = errors.New("observability: invalid measurements")
	ErrInvalidErrorCategory = errors.New("observability: invalid error category")
	ErrAlreadyTerminal      = errors.New("observability: span is already terminal")
)

var safeLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Scope holds an opaque high-entropy session identifier. NewScope only accepts
// canonical RFC4122 v4 UUIDs, whose source must use cryptographic randomness.
// Never use a Telegram chat, user, message, provider, or filesystem ID here.
type Scope struct {
	sessionID string
}

// NewScope validates the high-entropy scope before its raw value may be used
// only as a SHA-256 correlation input. The raw value is never written.
func NewScope(sessionID string) (Scope, error) {
	if !uuidV4.MatchString(strings.ToLower(sessionID)) {
		return Scope{}, ErrInvalidScope
	}
	return Scope{sessionID: sessionID}, nil
}

// Correlation deterministically derives an opaque ID for exactly one scope,
// occurrence, and stage. occurrenceID is an opaque stable input such as a
// durable operation ID: it is never persisted. stage is a static safe label
// and is the only operation-like value written to the event.
func (scope Scope) Correlation(stage, occurrenceID string) (string, error) {
	if scope.sessionID == "" {
		return "", ErrInvalidScope
	}
	if !safeLabel.MatchString(stage) || !safeLabel.MatchString(occurrenceID) {
		return "", ErrInvalidOperation
	}
	sum := sha256.Sum256([]byte("bria-observability-v1\x00" + scope.sessionID + "\x00" + occurrenceID + "\x00" + stage))
	return "c_" + hex.EncodeToString(sum[:]), nil
}

// Measurements contains the fixed optional timing/load schema for one terminal
// event. Nil means unavailable and is omitted. A non-nil zero is an observed
// zero. Negative durations and counters exceeding int64 are rejected before
// they reach safelog.
type Measurements struct {
	QueueWait      *time.Duration
	ProviderAccept *time.Duration
	FirstEvent     *time.Duration
	Total          *time.Duration
	HTTPToHeaders  *time.Duration
	HTTPTotal      *time.Duration
	RetryDelay     *time.Duration
	Bytes          *uint64
	OldestAge      *time.Duration
	ActiveTurns    *uint64
	UnknownCount   *uint64
	FailedCount    *uint64
}

// Duration marks a duration as measured, including a measured zero.
func Duration(value time.Duration) *time.Duration { return &value }

// Count marks a nonnegative counter as measured, including a measured zero.
func Count(value uint64) *uint64 { return &value }

// Recorder starts spans and emits at most one terminal event per span.
type Recorder struct {
	logger *safelog.Logger
}

// New creates the production instrumentation API. safelog exclusively owns
// UTC timestamps, redaction, retention, and persistence caps.
func New(logger *safelog.Logger) (*Recorder, error) {
	if logger == nil {
		return nil, ErrInvalidLogger
	}
	return &Recorder{logger: logger}, nil
}

// Span measures elapsed duration with time.Since. When its start value came
// from time.Now (as below), Go uses its monotonic clock component, insulating
// duration_ms from wall-clock adjustments.
type Span struct {
	logger      *safelog.Logger
	operation   string
	correlation string
	started     time.Time
	mu          sync.Mutex
	terminal    bool
}

// Start begins a timing span for a static safe stage and one opaque occurrence.
// occurrenceID is only a correlation hash input and must not be prompt text,
// a path, or other content. It may be a raw Telegram/message ID because it is
// never persisted; the high-entropy session scope prevents dictionary recovery.
func (recorder *Recorder) Start(scope Scope, stage, occurrenceID string) (*Span, error) {
	if recorder == nil || recorder.logger == nil {
		return nil, ErrInvalidLogger
	}
	correlation, err := scope.Correlation(stage, occurrenceID)
	if err != nil {
		return nil, err
	}
	return &Span{logger: recorder.logger, operation: stage, correlation: correlation, started: time.Now()}, nil
}

// Success writes the single success terminal event.
func (span *Span) Success(measurements Measurements) error {
	return span.finish("success", "", measurements)
}

// Failure writes the single error terminal event with a safe category only.
func (span *Span) Failure(errorCategory string, measurements Measurements) error {
	if !safeLabel.MatchString(errorCategory) {
		return ErrInvalidErrorCategory
	}
	return span.finish("error", errorCategory, measurements)
}

func (span *Span) finish(result, errorCategory string, measurements Measurements) error {
	if span == nil || span.logger == nil {
		return ErrInvalidLogger
	}
	fields, err := measurements.fields()
	if err != nil {
		return err
	}
	elapsed := time.Since(span.started)
	if elapsed < 0 {
		return ErrInvalidMeasurements
	}
	fields["duration_ms"] = strconv.FormatInt(elapsed.Milliseconds(), 10)
	fields["operation"] = span.operation
	span.mu.Lock()
	defer span.mu.Unlock()
	if span.terminal {
		return ErrAlreadyTerminal
	}
	event := safelog.Event{
		Class:         safelog.Service,
		Type:          "timing.terminal",
		EntityID:      span.correlation,
		Result:        result,
		ErrorCategory: errorCategory,
		Fields:        fields,
	}
	if err := span.logger.Write(event); err != nil {
		return err
	}
	span.terminal = true
	return nil
}

func (measurements Measurements) fields() (map[string]string, error) {
	values := []struct {
		key   string
		value *time.Duration
	}{
		{"queue_wait_ms", measurements.QueueWait}, {"provider_accept_ms", measurements.ProviderAccept},
		{"first_event_ms", measurements.FirstEvent}, {"total_ms", measurements.Total},
		{"http_to_headers_ms", measurements.HTTPToHeaders}, {"http_total_ms", measurements.HTTPTotal},
		{"retry_delay_ms", measurements.RetryDelay}, {"oldest_age_ms", measurements.OldestAge},
	}
	fields := make(map[string]string, 13)
	for _, value := range values {
		if value.value == nil {
			continue
		}
		if *value.value < 0 {
			return nil, ErrInvalidMeasurements
		}
		fields[value.key] = strconv.FormatInt(value.value.Milliseconds(), 10)
	}
	for _, value := range []struct {
		key   string
		value *uint64
	}{
		{"bytes", measurements.Bytes}, {"active_turns", measurements.ActiveTurns},
		{"unknown_count", measurements.UnknownCount}, {"failed_count", measurements.FailedCount},
	} {
		if value.value == nil {
			continue
		}
		if *value.value > uint64(^uint64(0)>>1) {
			return nil, ErrInvalidMeasurements
		}
		fields[value.key] = strconv.FormatUint(*value.value, 10)
	}
	return fields, nil
}
