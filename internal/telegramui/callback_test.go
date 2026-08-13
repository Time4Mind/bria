package telegramui

import (
	"strings"
	"testing"
)

func TestCallbackEncodingUsesOpaqueBoundedToken(t *testing.T) {
	callback := Callback{
		Action: ActionSelectSession,
		Token:  "opaque1234",
	}
	got, err := callback.Encode()
	if err != nil {
		t.Fatalf("Encode returned an error: %v", err)
	}
	if got != "session:opaque1234" {
		t.Fatalf("Encode = %q", got)
	}
	if len([]byte(got)) > MaxCallbackBytes {
		t.Fatalf("encoded callback is larger than Telegram's boundary")
	}
}

func TestCallbackRejectsEntityIDsAndOversizedTokens(t *testing.T) {
	tests := []Callback{
		{Action: ActionSelectSession, Token: "node/session"},
		{Action: ActionSelectSession, Token: OpaqueToken(strings.Repeat("x", 41))},
		{Action: Action("not allowed"), Token: "opaque"},
		{Action: Action("unknown"), Token: "opaque"},
	}
	for _, callback := range tests {
		if _, err := callback.Encode(); err == nil {
			t.Fatalf("Encode accepted invalid callback %#v", callback)
		}
	}
}

func TestCallbackDecodeRoundTripAndRejectsAmbiguousData(t *testing.T) {
	want := Callback{Action: ActionSelectSession, Token: "opaque-token"}
	encoded, err := want.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCallback(encoded)
	if err != nil || got != want {
		t.Fatalf("DecodeCallback(%q)=(%#v, %v)", encoded, got, err)
	}
	for _, invalid := range []string{"unknown:value", "session:", "session:a:b", " session:value"} {
		if _, err := DecodeCallback(invalid); err == nil {
			t.Fatalf("DecodeCallback accepted %q", invalid)
		}
	}
}
