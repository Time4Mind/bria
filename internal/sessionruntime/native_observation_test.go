package sessionruntime

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/runtimeprotocol"
)

func TestObservationCacheExactGenerationAndWaiterIsolation(t *testing.T) {
	id := domain.SessionID("logical")
	starter := &Starter{processes: map[domain.SessionID]*processRecord{}}
	updates := starter.NativeScreenUpdates()
	record := &processRecord{done: make(chan struct{}), binding: domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "native", Generation: 2}, nativeNotify: func() { starter.notifyNativeScreen(id) }}
	starter.processes[id] = record
	record.dispatchNative(wireMessage{Type: "ready", ProviderSessionID: "native"})
	waiter := make(chan wireResult, 1)
	record.nativeWaiters = map[string]chan wireResult{"command": waiter}
	observed := wireMessage{Type: "native_observation", ProviderSessionID: "native", Text: "pick model", FullText: "context and pick model", Hash: "hash", Model: "actual", Interactive: true}
	if !record.dispatchNative(observed) {
		t.Fatal("observation rejected")
	}
	if len(waiter) != 0 {
		t.Fatal("unsolicited observation satisfied correlated request")
	}
	snapshot, ok := starter.NativeScreen(id)
	if !ok || snapshot.Text != "pick model" || snapshot.FullText != "context and pick model" || !snapshot.Interactive {
		t.Fatalf("snapshot=%#v %v", snapshot, ok)
	}
	select {
	case got := <-updates:
		if got != id {
			t.Fatal(got)
		}
	default:
		t.Fatal("no render hint")
	}
	if !record.dispatchNative(observed) {
		t.Fatal("duplicate should be consumed")
	}
	if len(updates) != 0 {
		t.Fatal("duplicate generated render hint")
	}
	wrong := observed
	wrong.ProviderSessionID = "foreign"
	if record.dispatchNative(wrong) {
		t.Fatal("foreign accepted")
	}
	close(record.done)
	if _, ok := starter.NativeScreen(id); ok {
		t.Fatal("exited generation visible")
	}
	starter.processes[id] = &processRecord{done: make(chan struct{}), binding: domain.ProviderBinding{SessionID: "new", Generation: 3}}
	if _, ok := starter.NativeScreen(id); ok {
		t.Fatal("old cache leaked to replacement")
	}
}

func TestReadOutputRoutesObservationOutsideTurnStream(t *testing.T) {
	var input bytes.Buffer
	for _, message := range []runtimeprotocol.AdapterMessage{
		{Protocol: 1, Type: runtimeprotocol.TypeReady, ProviderSessionID: "native", Readiness: "protocol", Authentication: "unknown"},
		{Protocol: 1, Type: runtimeprotocol.TypeNativeObservation, ProviderSessionID: "native", Text: "picker", Hash: strings.Repeat("a", 64), Interactive: true},
		{Protocol: 1, Type: runtimeprotocol.TypeEvent, RequestID: "turn", Kind: "commentary", Text: "public commentary"},
	} {
		line, err := runtimeprotocol.EncodeAdapterLine(message, runtimeprotocol.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		input.Write(line)
	}
	record := &processRecord{}
	output := make(chan wireResult, 4)
	readOutput(io.NopCloser(&input), 64<<10, output, make(chan struct{}), make(chan struct{}), record.dispatchNative)
	results := []wireResult{}
	for result := range output {
		results = append(results, result)
	}
	if len(results) != 2 || results[0].message.Type != "ready" || results[1].message.Type != "event" || results[1].message.Text != "public commentary" {
		t.Fatalf("turn output=%#v", results)
	}
	if !record.hasNativeScreen || !record.nativeScreen.Interactive {
		t.Fatal("observation not cached")
	}
}
