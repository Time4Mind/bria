package callbacktoken

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCodecRoundTripFitsTelegramLimitAndOmitsCanonicalUUIDText(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader([]byte{1, 2, 3, 4, 5, 6, 7, 8}), func() time.Time { return now })
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	want := Fields{
		Action:    ActionSelectSession,
		SessionID: "00112233-4455-6677-8899-aabbccddeeff",
		Target:    0,
		ExpiresAt: now.Add(15 * time.Minute),
	}
	token, err := codec.Encode(want)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if got, wantLen := len(token), 64; got != wantLen {
		t.Fatalf("token length = %d, want %d", got, wantLen)
	}
	if len(token) > 64 {
		t.Fatalf("token exceeds Telegram callback_data limit: %d", len(token))
	}
	if strings.Contains(token, want.SessionID) || strings.Contains(token, "00112233") {
		t.Fatalf("token contains canonical UUID text: %q", token)
	}

	got, err := codec.Decode(token)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got != want {
		t.Fatalf("Decode() = %+v, want %+v", got, want)
	}
}

func TestCodecSupportsEveryTelegramUIActionWithoutChangingExistingWireValues(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	actions := []struct {
		name   string
		action Action
		wire   byte
		target int
	}{
		{"previous", ActionPreviousPage, 1, 1},
		{"next", ActionNextPage, 2, 1},
		{"latest", ActionLatestPage, 3, 0},
		{"select session", ActionSelectSession, 4, 0},
		{"stop", ActionStop, 5, 0},
		{"close", ActionClose, 6, 0},
		{"options", ActionOptions, 7, 0},
		{"screen", ActionScreen, 8, 0},
		{"resume", ActionResume, 9, 0},
		{"menu sessions", ActionMenuSessions, 10, 0},
		{"menu new", ActionMenuNew, 11, 0},
		{"menu archive", ActionMenuArchive, 12, 0},
		{"menu status", ActionMenuStatus, 13, 0},
		{"menu settings", ActionMenuSettings, 14, 0},
		{"menu back", ActionMenuBack, 15, 0},
		{"create codex", ActionCreateCodex, 16, 0},
		{"create claude", ActionCreateClaude, 17, 0},
		{"settings screen", ActionSettingsScreen, 18, 0},
		{"settings detail", ActionSettingsDetail, 19, 0},
		{"authorize codex", ActionAuthorizeCodex, 20, 0},
		{"authorize claude", ActionAuthorizeClaude, 21, 0},
		{"interaction choice", ActionInteractionChoice, 22, 1},
		{"interaction accept", ActionInteractionAccept, 23, 0},
		{"interaction decline", ActionInteractionDecline, 24, 0},
		{"interaction cancel", ActionInteractionCancel, 25, 0},
		{"outbound confirm delivered", ActionOutboundConfirmDelivered, 26, 0},
		{"outbound retry possible duplicate", ActionOutboundRetryPossibleDuplicate, 27, 0},
		{"callback effect confirmed", ActionCallbackEffectConfirmed, 28, 0},
		{"callback effect retry possible duplicate", ActionCallbackEffectRetryPossibleDuplicate, 29, 0},
		{"callback send confirmed", ActionCallbackSendConfirmed, 30, 0},
		{"callback send retry possible duplicate", ActionCallbackSendRetryPossibleDuplicate, 31, 0},
		{"interaction other", ActionInteractionOther, 32, 0},
		{"accepted turn assume completed", ActionAcceptedTurnAssumeCompleted, 33, 0},
		{"accepted turn retry possible duplicate", ActionAcceptedTurnRetryPossibleDuplicate, 34, 0},
		{"accepted turn cancel", ActionAcceptedTurnCancel, 35, 0},
		{"artifact retry", ActionArtifactRetry, 39, 0},
	}
	for _, tc := range actions {
		t.Run(tc.name, func(t *testing.T) {
			codec, err := New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader([]byte{1, 2, 3, 4, 5, 6, 7, 8}), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			want := Fields{
				Action:    tc.action,
				SessionID: "00112233-4455-6677-8899-aabbccddeeff",
				Target:    tc.target,
				ExpiresAt: now.Add(time.Minute),
			}
			token, err := codec.Encode(want)
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			raw, err := decodeWire(token)
			if err != nil {
				t.Fatal(err)
			}
			if raw[1] != tc.wire {
				t.Fatalf("wire action = %d, want %d", raw[1], tc.wire)
			}
			got, err := codec.Decode(token)
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if got != want {
				t.Fatalf("Decode() = %+v, want %+v", got, want)
			}
			if len(token) != EncodedLength || len(token) > 64 {
				t.Fatalf("token length = %d, want %d and <= 64", len(token), EncodedLength)
			}
		})
	}
}

func TestCodecRejectsTamperWrongKeyTruncationAndUnknownVersion(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key := bytes.Repeat([]byte{0x42}, 32)
	codec, _ := New(key, bytes.NewReader([]byte{1, 2, 3, 4, 5, 6, 7, 8}), func() time.Time { return now })
	token, err := codec.Encode(Fields{
		Action:    ActionNextPage,
		SessionID: "00112233-4455-6677-8899-aabbccddeeff",
		Target:    2,
		ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	tampered := []byte(token)
	if tampered[10] == 'A' {
		tampered[10] = 'B'
	} else {
		tampered[10] = 'A'
	}
	wrongKey, _ := New(bytes.Repeat([]byte{0x24}, 32), bytes.NewReader(nil), func() time.Time { return now })

	for name, tc := range map[string]struct {
		codec *Codec
		token string
	}{
		"tampered":       {codec, string(tampered)},
		"wrong key":      {wrongKey, token},
		"truncated":      {codec, token[:len(token)-1]},
		"invalid base64": {codec, token[:len(token)-1] + "!"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tc.codec.Decode(tc.token); err == nil {
				t.Fatal("Decode() error = nil")
			}
		})
	}

	raw, err := decodeWire(token)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = 2
	resignWire(raw, key)
	unknownVersion := encodeWire(raw)
	if _, err := codec.Decode(unknownVersion); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Decode() error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestCodecRejectsInvalidConstructionAndFields(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	if _, err := New(bytes.Repeat([]byte{1}, 31), bytes.NewReader(nil), func() time.Time { return now }); !errors.Is(err, ErrWeakKey) {
		t.Fatalf("New() error = %v, want ErrWeakKey", err)
	}

	codec, _ := New(bytes.Repeat([]byte{1}, 32), bytes.NewReader(bytes.Repeat([]byte{1}, 64)), func() time.Time { return now })
	base := Fields{
		Action:    ActionPreviousPage,
		SessionID: "00112233-4455-6677-8899-aabbccddeeff",
		Target:    1,
		ExpiresAt: now.Add(time.Minute),
	}
	tests := map[string]Fields{
		"unknown action":            withAction(base, Action(99)),
		"arbitrary session":         withSession(base, "provider-session-id"),
		"noncanonical UUID":         withSession(base, "00112233445566778899aabbccddeeff"),
		"negative target":           withTarget(base, -1),
		"large target":              withTarget(base, 1<<16),
		"already expired":           withExpiry(base, now),
		"previous page zero target": withTarget(base, 0),
		"next page zero target":     withTarget(withAction(base, ActionNextPage), 0),
		"session nonzero target":    withTarget(withAction(base, ActionSelectSession), 1),
		"latest nonzero target":     withAction(base, ActionLatestPage),
		"stop nonzero target":       withAction(base, ActionStop),
		"close nonzero target":      withAction(base, ActionClose),
		"options nonzero target":    withAction(base, ActionOptions),
		"screen nonzero target":     withAction(base, ActionScreen),
		"resume nonzero target":     withAction(base, ActionResume),
	}
	for name, fields := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := codec.Encode(fields); err == nil {
				t.Fatal("Encode() error = nil")
			}
		})
	}
}

func TestCodecRejectsExpiredTokenAndUnknownActionOnWire(t *testing.T) {
	issuedAt := time.Unix(1_800_000_000, 0).UTC()
	key := bytes.Repeat([]byte{0x42}, 32)
	codec, _ := New(key, bytes.NewReader([]byte{1, 2, 3, 4, 5, 6, 7, 8}), func() time.Time { return issuedAt })
	token, err := codec.Encode(Fields{
		Action:    ActionLatestPage,
		SessionID: "00112233-4455-6677-8899-aabbccddeeff",
		Target:    0,
		ExpiresAt: issuedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	codec.now = func() time.Time { return issuedAt.Add(2 * time.Second) }
	if _, err := codec.Decode(token); !errors.Is(err, ErrExpired) {
		t.Fatalf("Decode() error = %v, want ErrExpired", err)
	}

	raw, _ := decodeWire(token)
	raw[1] = 99
	resignWire(raw, key)
	if _, err := codec.Decode(encodeWire(raw)); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("Decode() error = %v, want ErrUnknownAction", err)
	}
}

func TestCodecRejectsAuthenticatedWireTargetThatViolatesActionPolicy(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key := bytes.Repeat([]byte{0x42}, 32)
	codec, _ := New(key, bytes.NewReader(bytes.Repeat([]byte{1}, 128)), func() time.Time { return now })

	for _, tc := range []struct {
		name          string
		action        Action
		encodedTarget int
		wireTarget    uint16
	}{
		{"previous requires positive", ActionPreviousPage, 1, 0},
		{"next requires positive", ActionNextPage, 1, 0},
		{"session requires zero", ActionSelectSession, 0, 1},
		{"latest requires zero", ActionLatestPage, 0, 1},
		{"stop requires zero", ActionStop, 0, 1},
		{"close requires zero", ActionClose, 0, 1},
		{"options requires zero", ActionOptions, 0, 1},
		{"screen requires zero", ActionScreen, 0, 1},
		{"resume requires zero", ActionResume, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, err := codec.Encode(Fields{
				Action:    tc.action,
				SessionID: "00112233-4455-6677-8899-aabbccddeeff",
				Target:    tc.encodedTarget,
				ExpiresAt: now.Add(time.Minute),
			})
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			raw, err := decodeWire(token)
			if err != nil {
				t.Fatal(err)
			}
			binary.BigEndian.PutUint16(raw[18:20], tc.wireTarget)
			resignWire(raw, key)
			if _, err := codec.Decode(encodeWire(raw)); !errors.Is(err, ErrInvalidFields) {
				t.Fatalf("Decode() error = %v, want ErrInvalidFields", err)
			}
		})
	}
}

func TestCodecRequiresNonceEntropy(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, _ := New(bytes.Repeat([]byte{1}, 32), bytes.NewReader([]byte{1, 2}), func() time.Time { return now })
	_, err := codec.Encode(Fields{
		Action:    ActionLatestPage,
		SessionID: "00112233-4455-6677-8899-aabbccddeeff",
		ExpiresAt: now.Add(time.Minute),
	})
	if err == nil {
		t.Fatal("Encode() error = nil")
	}
}

func withAction(f Fields, v Action) Fields    { f.Action = v; return f }
func withSession(f Fields, v string) Fields   { f.SessionID = v; return f }
func withTarget(f Fields, v int) Fields       { f.Target = v; return f }
func withExpiry(f Fields, v time.Time) Fields { f.ExpiresAt = v; return f }

func decodeWire(token string) ([]byte, error) {
	return base64.RawURLEncoding.Strict().DecodeString(token)
}

func encodeWire(wire []byte) string {
	return base64.RawURLEncoding.EncodeToString(wire)
}

func resignWire(wire, key []byte) {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(wire[:payloadSize])
	copy(wire[payloadSize:], mac.Sum(nil)[:tagSize])
}
