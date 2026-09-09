package callbacktoken_test

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	"bria/internal/callbacktoken"
)

func TestCommandLinesWirePreservesOutputIDAndRejectsForgedOrExpiredToken(t *testing.T) {
	if callbacktoken.ActionSettingsTechnicalOutputLines != 87 || callbacktoken.ActionSettingsTechnicalCommandLines != 88 {
		t.Fatal("incompatible wire IDs")
	}
	testSettingsWire(t, callbacktoken.ActionSettingsTechnicalCommandLines)
}

func TestHiddenDirectoriesWireRejectsForgedOrExpiredToken(t *testing.T) {
	if callbacktoken.ActionSettingsHiddenDirectories != 89 {
		t.Fatal("incompatible hidden-directory wire ID")
	}
	testSettingsWire(t, callbacktoken.ActionSettingsHiddenDirectories)
}

func testSettingsWire(t *testing.T, action callbacktoken.Action) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 128)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	want := callbacktoken.Fields{Action: action, SessionID: "00000000-0000-0000-0000-000000000001", ExpiresAt: now.Add(time.Minute)}
	token, err := codec.Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := codec.Decode(token); err != nil || got != want || len(token) > 64 {
		t.Fatalf("roundtrip=%+v err=%v", got, err)
	}
	for _, target := range []int{1, 3, 5, 10, 20} {
		invalid := want
		invalid.Target = target
		if _, err := codec.Encode(invalid); err == nil {
			t.Fatalf("nonzero target %d accepted", target)
		}
	}
	encoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	encoded[len(encoded)-1] ^= 1
	if _, err := codec.Decode(base64.RawURLEncoding.EncodeToString(encoded)); err == nil {
		t.Fatal("forged callback authenticated")
	}
	now = now.Add(2 * time.Minute)
	if _, err := codec.Decode(token); err == nil {
		t.Fatal("expired callback accepted")
	}
}
