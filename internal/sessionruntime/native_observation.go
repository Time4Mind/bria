package sessionruntime

import (
	"bria/internal/domain"
	"bria/internal/nativecontrolport"
)

// NativeScreenProvider exposes cached snapshots; coalesced updates are hints,
// not history. Reading a snapshot never captures a terminal or sends input.
type NativeScreenProvider = nativecontrolport.ScreenProvider

func (starter *Starter) NativeScreen(id domain.SessionID) (NativeSnapshot, bool) {
	starter.mu.Lock()
	defer starter.mu.Unlock()
	record, ok := starter.processes[id]
	if !ok || record.binding.SessionID == "" {
		return NativeSnapshot{}, false
	}
	select {
	case <-record.done:
		return NativeSnapshot{}, false
	default:
	}
	record.nativeMu.Lock()
	defer record.nativeMu.Unlock()
	if record.nativeIdentity != record.binding.SessionID {
		return NativeSnapshot{}, false
	}
	snapshot := record.nativeScreen
	snapshot.Generation = record.binding.Generation
	return snapshot, record.hasNativeScreen
}

func (starter *Starter) NativeScreenUpdates() <-chan domain.SessionID {
	starter.mu.Lock()
	defer starter.mu.Unlock()
	if starter.nativeUpdates == nil {
		starter.nativeUpdates = make(chan domain.SessionID, 64)
	}
	return starter.nativeUpdates
}

func (starter *Starter) notifyNativeScreen(id domain.SessionID) {
	starter.mu.Lock()
	defer starter.mu.Unlock()
	if starter.nativeUpdates != nil {
		select {
		case starter.nativeUpdates <- id:
		default:
		}
	}
}

func (record *processRecord) observeNative(message wireMessage) bool {
	record.nativeMu.Lock()
	if message.Type == "ready" {
		if record.nativeIdentity == "" {
			record.nativeIdentity = message.ProviderSessionID
		}
		record.nativeMu.Unlock()
		return false // ready must still reach handshake
	}
	if record.nativeIdentity == "" || message.ProviderSessionID != record.nativeIdentity {
		record.nativeMu.Unlock()
		return false
	}
	next := NativeSnapshot{Text: message.Text, FullText: message.FullText, Hash: message.Hash, Model: message.Model, Interactive: message.Interactive, ProviderSessionID: message.ProviderSessionID, Generation: record.generation}
	changed := !record.hasNativeScreen || next != record.nativeScreen
	record.nativeScreen = next
	record.hasNativeScreen = true
	if next.Model != "" {
		record.nativeModel = next.Model
	}
	notify := record.nativeNotify
	record.nativeMu.Unlock()
	if changed && notify != nil {
		notify()
	}
	return true
}
