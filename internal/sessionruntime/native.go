package sessionruntime

import (
	"context"
	"errors"
	"fmt"

	"bria/internal/domain"
	"bria/internal/nativecontrolport"
	"bria/internal/runtimeprotocol"
)

type NativeRequest = nativecontrolport.Request
type NativeSnapshot = nativecontrolport.Snapshot
type NativeController = nativecontrolport.Controller

// NativeModelProvider reads metadata confirmed by the exact live adapter
// generation. It never captures a terminal or waits for provider I/O.
type NativeModelProvider interface {
	NativeModel(domain.SessionID) (string, bool)
}

func (starter *Starter) NativeModel(id domain.SessionID) (string, bool) {
	starter.mu.Lock()
	defer starter.mu.Unlock()
	record, ok := starter.processes[id]
	if !ok || record.binding.SessionID == "" {
		return "", false
	}
	select {
	case <-record.done:
		return "", false
	default:
	}
	record.nativeMu.Lock()
	defer record.nativeMu.Unlock()
	return record.nativeModel, record.nativeModel != ""
}

var ErrNativeStale = errors.New("native screen changed")
var ErrNativeUnavailable = errors.New("native control unavailable")

// NativeControl is independent of inference turns. It requires an exact
// correlated snapshot receipt; writing bytes is never reported as acceptance.
func (starter *Starter) NativeControl(ctx context.Context, id domain.SessionID, request NativeRequest) (NativeSnapshot, error) {
	if ctx == nil {
		return NativeSnapshot{}, errors.New("native context is required")
	}
	if err := ctx.Err(); err != nil {
		return NativeSnapshot{}, err
	}
	requestID := fmt.Sprintf("n-%d", starter.requestSequence.Add(1))
	encoded, err := runtimeprotocol.EncodeParentLine(runtimeprotocol.ParentMessage{
		Protocol: ProtocolVersion, Type: runtimeprotocol.TypeNativeControl, RequestID: requestID,
		Command: request.Command, Key: request.Key, ExpectedHash: request.ExpectedHash,
	}, runtimeprotocol.Limits{MaxLineBytes: starter.maxLineBytes, MaxTextBytes: starter.maxTextBytes})
	if err != nil {
		return NativeSnapshot{}, ErrProtocol
	}
	starter.mu.Lock()
	record, ok := starter.processes[id]
	bound := ok && record.binding.SessionID != ""
	starter.mu.Unlock()
	if !bound {
		return NativeSnapshot{}, ErrSessionNotTracked
	}
	if request.ExpectedProviderSessionID != "" && (request.ExpectedProviderSessionID != record.binding.SessionID || request.ExpectedGeneration != record.binding.Generation) {
		return NativeSnapshot{}, ErrNativeStale
	}
	waiter := make(chan wireResult, 1)
	record.nativeMu.Lock()
	if record.nativeWaiters == nil {
		record.nativeWaiters = make(map[string]chan wireResult)
	}
	if len(record.nativeWaiters) >= 16 {
		record.nativeMu.Unlock()
		return NativeSnapshot{}, ErrNativeUnavailable
	}
	record.nativeWaiters[requestID] = waiter
	record.nativeMu.Unlock()
	defer func() { record.nativeMu.Lock(); delete(record.nativeWaiters, requestID); record.nativeMu.Unlock() }()
	if err := starter.writeEncodedContext(ctx, record, encoded); err != nil {
		return NativeSnapshot{}, err
	}
	select {
	case result := <-waiter:
		message := result.message
		snapshot := NativeSnapshot{Text: message.Text, FullText: message.FullText, Hash: message.Hash, Model: message.Model, Interactive: message.Interactive, ProviderSessionID: record.binding.SessionID, Generation: record.binding.Generation}
		if len(snapshot.Text) > starter.maxTextBytes {
			starter.killAndWait(record)
			return NativeSnapshot{}, ErrTextTooLarge
		}
		switch message.ErrorCode {
		case "stale":
			return snapshot, ErrNativeStale
		case "unavailable":
			return snapshot, ErrNativeUnavailable
		default:
			return snapshot, nil
		}
	case <-ctx.Done():
		// A cancelled write may already have reached the CLI. Retire this
		// generation rather than let a late receipt mutate a later screen.
		starter.killAndWait(record)
		return NativeSnapshot{}, ctx.Err()
	case <-record.done:
		return NativeSnapshot{}, ErrNativeUnavailable
	}
}

func (record *processRecord) dispatchNative(message wireMessage) bool {
	if message.Type == string(runtimeprotocol.TypeClosed) {
		record.nativeMu.Lock()
		defer record.nativeMu.Unlock()
		if !record.persistentTerminal || !record.closeRequested.Load() || message.ProviderSessionID != record.nativeIdentity || record.terminalClosed.Load() {
			return false
		}
		record.terminalClosed.Store(true)
		return true
	}
	if message.Type == string(runtimeprotocol.TypeReady) || message.Type == string(runtimeprotocol.TypeNativeObservation) {
		return record.observeNative(message)
	}
	record.nativeMu.Lock()
	defer record.nativeMu.Unlock()
	waiter, ok := record.nativeWaiters[message.RequestID]
	if !ok {
		return false
	}
	select {
	case waiter <- wireResult{message: message}:
		record.nativeScreen = NativeSnapshot{Text: message.Text, FullText: message.FullText, Hash: message.Hash, Model: message.Model, Interactive: message.Interactive, ProviderSessionID: record.binding.SessionID, Generation: record.binding.Generation}
		record.hasNativeScreen = true
		if message.Model != "" {
			record.nativeModel = message.Model
		}
		delete(record.nativeWaiters, message.RequestID)
		return true
	default:
		return false
	}
}
