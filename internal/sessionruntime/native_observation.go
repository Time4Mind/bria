package sessionruntime

import "bria/internal/domain"

// NativeScreenProvider exposes a cached exact live-process snapshot. Reading it
// never sends keys, captures a terminal, or waits for provider I/O. Updates are
// coalescible hints, not a history stream; consumers always reread NativeScreen.
type NativeScreenProvider interface {
	NativeScreen(domain.SessionID) (NativeSnapshot, bool)
	NativeScreenUpdates() <-chan domain.SessionID
}

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
	return record.nativeScreen, record.hasNativeScreen
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
	next := NativeSnapshot{Text: message.Text, FullText: message.FullText, Hash: message.Hash, Model: message.Model, Interactive: message.Interactive}
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
