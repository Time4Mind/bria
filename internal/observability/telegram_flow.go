package observability

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"

	"bria/internal/safelog"
	"bria/internal/telegramflow"
)

type TelegramFlowObserver struct {
	logger  *safelog.Logger
	events  chan telegramflow.TraceEvent
	done    chan struct{}
	once    sync.Once
	mu      sync.RWMutex
	closed  bool
	dropped atomic.Uint64
	key     [32]byte
}

func NewTelegramFlowObserver(logger *safelog.Logger) (*TelegramFlowObserver, error) {
	if logger == nil {
		return nil, errors.New("Telegram flow observer requires a safe logger")
	}
	observer := &TelegramFlowObserver{logger: logger, events: make(chan telegramflow.TraceEvent, 4096), done: make(chan struct{})}
	if _, err := rand.Read(observer.key[:]); err != nil {
		return nil, errors.New("generate Telegram flow correlation key")
	}
	go observer.run()
	return observer, nil
}

func (observer *TelegramFlowObserver) ObserveTelegramFlow(_ context.Context, event telegramflow.TraceEvent) {
	if observer == nil || observer.logger == nil {
		return
	}
	observer.mu.RLock()
	defer observer.mu.RUnlock()
	if observer.closed {
		return
	}
	select {
	case observer.events <- event:
	default:
		observer.dropped.Add(1)
	}
}

func (observer *TelegramFlowObserver) Close() {
	if observer == nil {
		return
	}
	observer.once.Do(func() {
		observer.mu.Lock()
		observer.closed = true
		close(observer.events)
		observer.mu.Unlock()
		<-observer.done
	})
}

func (observer *TelegramFlowObserver) run() {
	defer close(observer.done)
	for event := range observer.events {
		observer.writeDropped()
		fields := map[string]string{"stage": event.Stage, "duration_ms": strconv.FormatInt(event.Duration.Milliseconds(), 10)}
		if event.UpdateKind != "" {
			fields["entity_kind"] = string(event.UpdateKind)
		}
		if event.Action != "" {
			fields["action"] = string(event.Action)
		}
		if event.Effect != "" {
			fields["operation"] = string(event.Effect)
		}
		category := ""
		if event.Error != "" {
			category = "telegram_flow"
		}
		_ = observer.logger.Write(safelog.Event{
			Class: safelog.Detailed, Type: "telegram.flow_stage", EntityID: observer.correlation(event.OperationID),
			Result: event.Result, ErrorCategory: category, Error: event.Error, Fields: fields,
		})
	}
	observer.writeDropped()
}

func (observer *TelegramFlowObserver) correlation(operationID string) string {
	if operationID == "" {
		return ""
	}
	digest := hmac.New(sha256.New, observer.key[:])
	_, _ = digest.Write([]byte(operationID))
	return "c_" + hex.EncodeToString(digest.Sum(nil))
}

func (observer *TelegramFlowObserver) writeDropped() {
	count := observer.dropped.Swap(0)
	if count == 0 {
		return
	}
	_ = observer.logger.Write(safelog.Event{
		Class: safelog.Service, Type: "telegram.flow_trace_dropped", Result: "buffer_full",
		Fields: map[string]string{"count": strconv.FormatUint(count, 10)},
	})
}
