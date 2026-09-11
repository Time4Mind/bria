package observability

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"bria/internal/controllertelemetry"
	"bria/internal/safelog"
	"bria/internal/telegramtrace"
)

type TelegramFlowObserver struct {
	logger   *safelog.Logger
	events   chan telegramtrace.Event
	done     chan struct{}
	once     sync.Once
	mu       sync.RWMutex
	closed   bool
	dropped  atomic.Uint64
	key      [32]byte
	sequence uint64 // Owned by run; persisted order includes dropped-event records.
	failed   uint64 // Writer failures since the last successful record.
}

func NewTelegramFlowObserver(logger *safelog.Logger) (*TelegramFlowObserver, error) {
	if logger == nil {
		return nil, errors.New("Telegram flow observer requires a safe logger")
	}
	observer := &TelegramFlowObserver{logger: logger, events: make(chan telegramtrace.Event, 4096), done: make(chan struct{})}
	if _, err := rand.Read(observer.key[:]); err != nil {
		return nil, errors.New("generate Telegram flow correlation key")
	}
	ready := telegramtrace.Record(telegramtrace.Event{}, observer.key[:], 0)
	if err := logger.Write(safelog.Event{
		Class: safelog.Service, Type: "telegram.flow_ready", Result: "ready",
		Fields: map[string]string{"version": "3", "run_ref": ready.Fields["run_ref"], "sequence": "0"},
	}); err != nil {
		return nil, err
	}
	go observer.run()
	return observer, nil
}

func (observer *TelegramFlowObserver) ObserveControllerEvent(ctx context.Context, event controllertelemetry.Event) {
	if event.OperationID == "" {
		event.OperationID = controllertelemetry.Operation(ctx)
	}
	observer.ObserveTelegramFlow(ctx, telegramtrace.Controller(event))
}

func (observer *TelegramFlowObserver) ObserveTelegramFlow(_ context.Context, event telegramtrace.Event) {
	if observer == nil || observer.logger == nil {
		return
	}
	observer.mu.RLock()
	defer observer.mu.RUnlock()
	if observer.closed {
		return
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	event.ButtonIDs = append([]string(nil), event.ButtonIDs...)
	select {
	case observer.events <- event:
	default:
		observer.dropped.Add(1)
	}
}

func (observer *TelegramFlowObserver) InputRef(operation string) string {
	if observer == nil {
		return ""
	}
	return telegramtrace.Record(telegramtrace.Event{OperationID: operation}, observer.key[:], 0).Fields["input_ref"]
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
		observer.sequence++
		observer.write(telegramtrace.Record(event, observer.key[:], observer.sequence))
	}
	observer.writeDropped()
}

func (observer *TelegramFlowObserver) writeDropped() {
	count := observer.dropped.Swap(0)
	if count == 0 {
		return
	}
	observer.sequence++
	record := telegramtrace.Record(telegramtrace.Event{}, observer.key[:], observer.sequence)
	observer.write(safelog.Event{
		Class: safelog.Service, Type: "telegram.flow_trace_dropped", Result: "buffer_full",
		Fields: map[string]string{"count": strconv.FormatUint(count, 10), "run_ref": record.Fields["run_ref"], "sequence": record.Fields["sequence"]},
	})
}

func (observer *TelegramFlowObserver) write(record safelog.Event) {
	if observer.failed > 0 {
		record.Fields["failed_count"] = strconv.FormatUint(observer.failed, 10)
	}
	if err := observer.logger.Write(record); err != nil {
		observer.failed++
		return
	}
	observer.failed = 0
}
