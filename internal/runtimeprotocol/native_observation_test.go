package runtimeprotocol

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestNativeObservationSeparateBoundedEnvelope(t *testing.T) {
	sum := sha256.Sum256([]byte("menu"))
	message := AdapterMessage{Protocol: Version, Type: TypeNativeObservation, ProviderSessionID: "exact-native", Text: "menu", Hash: hex.EncodeToString(sum[:]), Interactive: true}
	encoded, err := EncodeAdapterLine(message, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeAdapterLine(encoded, Limits{})
	if err != nil || got.RequestID != "" || got.ProviderSessionID != message.ProviderSessionID || !got.Interactive {
		t.Fatalf("observation=%#v %v", got, err)
	}
	for _, mutate := range []func(*AdapterMessage){
		func(m *AdapterMessage) { m.RequestID = "command-reply" },
		func(m *AdapterMessage) { m.ProviderSessionID = "" },
		func(m *AdapterMessage) { m.Hash = "corrupt" },
		func(m *AdapterMessage) { m.Text = strings.Repeat("x", (24<<10)+1) },
		func(m *AdapterMessage) { m.Kind = "commentary" },
	} {
		bad := message
		mutate(&bad)
		if _, err := EncodeAdapterLine(bad, Limits{}); err == nil {
			t.Fatal("invalid observation accepted")
		}
	}
}
