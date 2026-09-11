package nativeadapter

import (
	"bytes"
	"testing"
	"time"

	"bria/internal/runtimeprotocol"
)

func TestObservationCarriesChangedOrdinaryScreenAndModel(t *testing.T) {
	output := &bytes.Buffer{}
	a := &adapter{id: fixtureSession, model: "model-a", output: output}
	now := time.Now()
	if err := a.emitScreenObservation("ordinary response", now); err != nil {
		t.Fatal(err)
	}
	first := output.Len()
	if err := a.emitScreenObservation("more ordinary response", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if output.Len() == first {
		t.Fatal("ordinary screen change was not cached for Screen")
	}
	a.model = "model-b"
	if err := a.emitScreenObservation("more ordinary response", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("frames=%d", len(lines))
	}
	frame, err := runtimeprotocol.DecodeAdapterLine(lines[2], runtimeprotocol.Limits{})
	if err != nil || frame.Type != runtimeprotocol.TypeNativeObservation || frame.Model != "model-b" || frame.RequestID != "" || frame.FullText != "more ordinary response" {
		t.Fatalf("frame=%#v %v", frame, err)
	}
}

func TestUnchangedObservationAdvancesCaptureThrottle(t *testing.T) {
	output := &bytes.Buffer{}
	a := &adapter{id: fixtureSession, model: "model-a", output: output}
	first := time.Unix(10, 0)
	if err := a.emitScreenObservation("stable screen", first); err != nil {
		t.Fatal(err)
	}
	second := first.Add(time.Second)
	if err := a.emitScreenObservation("stable screen", second); err != nil {
		t.Fatal(err)
	}
	if !a.observation.sentAt.Equal(second) {
		t.Fatalf("unchanged capture timestamp = %s, want %s", a.observation.sentAt, second)
	}
	if lines := bytes.Count(output.Bytes(), []byte{'\n'}); lines != 1 {
		t.Fatalf("unchanged screen emitted %d frames, want 1", lines)
	}
}
