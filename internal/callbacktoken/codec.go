// Package callbacktoken encodes authenticated, compact Telegram callback data.
// Tokens provide integrity, not confidentiality: their base64url payload is
// decodable and must never contain secrets.
package callbacktoken

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

const (
	wireVersion = byte(1)
	wireSize    = 48
	payloadSize = 32
	tagSize     = 16
	nonceSize   = 8

	// EncodedLength is the exact callback_data length produced by this version.
	EncodedLength = 64
	// MaxTarget is the largest page target representable on wire.
	MaxTarget = math.MaxUint16
)

var (
	ErrWeakKey            = errors.New("callback token key must contain at least 32 bytes")
	ErrInvalidToken       = errors.New("invalid callback token")
	ErrUnsupportedVersion = errors.New("unsupported callback token version")
	ErrUnknownAction      = errors.New("unknown callback token action")
	ErrExpired            = errors.New("callback token expired")
	ErrInvalidFields      = errors.New("invalid callback token fields")
)

// Action identifies a callback without exposing Telegram copy in the token.
type Action uint8

const (
	ActionPreviousPage                         Action = 1
	ActionNextPage                             Action = 2
	ActionLatestPage                           Action = 3
	ActionSelectSession                        Action = 4
	ActionStop                                 Action = 5
	ActionClose                                Action = 6
	ActionOptions                              Action = 7
	ActionScreen                               Action = 8
	ActionResume                               Action = 9
	ActionMenuSessions                         Action = 10
	ActionMenuNew                              Action = 11
	ActionMenuArchive                          Action = 12
	ActionMenuStatus                           Action = 13
	ActionMenuSettings                         Action = 14
	ActionMenuBack                             Action = 15
	ActionCreateCodex                          Action = 16
	ActionCreateClaude                         Action = 17
	ActionSettingsScreen                       Action = 18
	ActionSettingsDetail                       Action = 19
	ActionAuthorizeCodex                       Action = 20
	ActionAuthorizeClaude                      Action = 21
	ActionInteractionChoice                    Action = 22
	ActionInteractionAccept                    Action = 23
	ActionInteractionDecline                   Action = 24
	ActionInteractionCancel                    Action = 25
	ActionOutboundConfirmDelivered             Action = 26
	ActionOutboundRetryPossibleDuplicate       Action = 27
	ActionCallbackEffectConfirmed              Action = 28
	ActionCallbackEffectRetryPossibleDuplicate Action = 29
	ActionCallbackSendConfirmed                Action = 30
	ActionCallbackSendRetryPossibleDuplicate   Action = 31
	ActionInteractionOther                     Action = 32
	ActionAcceptedTurnAssumeCompleted          Action = 33
	ActionAcceptedTurnRetryPossibleDuplicate   Action = 34
	ActionAcceptedTurnCancel                   Action = 35
	ActionStatusRecoveryAssumeDelivered        Action = 36
	ActionStatusRecoveryRetryPossibleDuplicate Action = 37
	ActionStatusRecoveryCancel                 Action = 38
	ActionArtifactRetry                        Action = 39
)

// Fields is the semantic callback payload. SessionID identifies the selected
// logical session for ActionSelectSession. Target is a positive page number for
// previous/next and zero for every other action. ExpiresAt has one-second
// precision.
type Fields struct {
	Action    Action
	SessionID string
	Target    int
	ExpiresAt time.Time
}

// Codec authenticates callback tokens. A token can be replayed until ExpiresAt;
// consumers must enforce one-time semantics themselves when an action needs it.
type Codec struct {
	key    []byte
	random io.Reader
	now    func() time.Time
}

// New constructs a codec. Passing nil random or now selects secure defaults.
func New(key []byte, random io.Reader, now func() time.Time) (*Codec, error) {
	if len(key) < 32 {
		return nil, ErrWeakKey
	}
	if random == nil {
		random = rand.Reader
	}
	if now == nil {
		now = time.Now
	}
	return &Codec{
		key:    append([]byte(nil), key...),
		random: random,
		now:    now,
	}, nil
}

// Encode returns a 64-byte unpadded base64url token.
func (c *Codec) Encode(fields Fields) (string, error) {
	if !validAction(fields.Action) {
		return "", ErrUnknownAction
	}
	uuid, err := parseCanonicalUUID(fields.SessionID)
	if err != nil {
		return "", fmt.Errorf("%w: session ID", ErrInvalidFields)
	}
	if fields.Target < 0 || fields.Target > MaxTarget {
		return "", fmt.Errorf("%w: target", ErrInvalidFields)
	}
	if !validTarget(fields.Action, fields.Target) {
		return "", fmt.Errorf("%w: target for action", ErrInvalidFields)
	}
	if fields.ExpiresAt.Nanosecond() != 0 {
		return "", fmt.Errorf("%w: expiry precision", ErrInvalidFields)
	}
	expires := fields.ExpiresAt.Unix()
	if expires <= c.now().Unix() || expires < 0 || expires > math.MaxUint32 {
		return "", fmt.Errorf("%w: expiry", ErrInvalidFields)
	}

	wire := make([]byte, wireSize)
	wire[0] = wireVersion
	wire[1] = byte(fields.Action)
	copy(wire[2:18], uuid[:])
	binary.BigEndian.PutUint16(wire[18:20], uint16(fields.Target))
	binary.BigEndian.PutUint32(wire[20:24], uint32(expires))
	if _, err := io.ReadFull(c.random, wire[24:24+nonceSize]); err != nil {
		return "", fmt.Errorf("callback token nonce: %w", err)
	}
	copy(wire[payloadSize:], authenticationTag(c.key, wire[:payloadSize]))

	token := base64.RawURLEncoding.EncodeToString(wire)
	if len(token) != EncodedLength {
		return "", ErrInvalidToken
	}
	return token, nil
}

// Decode authenticates and validates a callback token before returning fields.
func (c *Codec) Decode(token string) (Fields, error) {
	if len(token) != EncodedLength {
		return Fields{}, ErrInvalidToken
	}
	wire, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(wire) != wireSize {
		return Fields{}, ErrInvalidToken
	}
	wantTag := authenticationTag(c.key, wire[:payloadSize])
	if !hmac.Equal(wire[payloadSize:], wantTag) {
		return Fields{}, ErrInvalidToken
	}
	if wire[0] != wireVersion {
		return Fields{}, ErrUnsupportedVersion
	}
	action := Action(wire[1])
	if !validAction(action) {
		return Fields{}, ErrUnknownAction
	}
	target := int(binary.BigEndian.Uint16(wire[18:20]))
	if !validTarget(action, target) {
		return Fields{}, ErrInvalidFields
	}
	expires := time.Unix(int64(binary.BigEndian.Uint32(wire[20:24])), 0).UTC()
	if expires.Unix() <= c.now().Unix() {
		return Fields{}, ErrExpired
	}

	var uuid [16]byte
	copy(uuid[:], wire[2:18])
	return Fields{
		Action:    action,
		SessionID: formatUUID(uuid),
		Target:    target,
		ExpiresAt: expires,
	}, nil
}

func validAction(action Action) bool {
	switch action {
	case ActionPreviousPage, ActionNextPage, ActionLatestPage, ActionSelectSession,
		ActionStop, ActionClose, ActionOptions, ActionScreen, ActionResume,
		ActionMenuSessions, ActionMenuNew, ActionMenuArchive, ActionMenuStatus,
		ActionMenuSettings, ActionMenuBack, ActionCreateCodex, ActionCreateClaude,
		ActionSettingsScreen, ActionSettingsDetail, ActionAuthorizeCodex, ActionAuthorizeClaude:
		return true
	case ActionInteractionChoice, ActionInteractionAccept, ActionInteractionDecline, ActionInteractionCancel,
		ActionOutboundConfirmDelivered, ActionOutboundRetryPossibleDuplicate,
		ActionCallbackEffectConfirmed, ActionCallbackEffectRetryPossibleDuplicate,
		ActionCallbackSendConfirmed, ActionCallbackSendRetryPossibleDuplicate, ActionInteractionOther,
		ActionAcceptedTurnAssumeCompleted, ActionAcceptedTurnRetryPossibleDuplicate, ActionAcceptedTurnCancel,
		ActionStatusRecoveryAssumeDelivered, ActionStatusRecoveryRetryPossibleDuplicate, ActionStatusRecoveryCancel, ActionArtifactRetry:
		return true
	default:
		return false
	}
}

func validTarget(action Action, target int) bool {
	switch action {
	case ActionPreviousPage, ActionNextPage:
		return target > 0 && target <= MaxTarget
	case ActionInteractionChoice:
		return target > 0 && target <= MaxTarget
	case ActionLatestPage, ActionSelectSession, ActionStop, ActionClose, ActionOptions, ActionScreen, ActionResume,
		ActionMenuSessions, ActionMenuNew, ActionMenuArchive, ActionMenuStatus,
		ActionMenuSettings, ActionMenuBack, ActionCreateCodex, ActionCreateClaude,
		ActionSettingsScreen, ActionSettingsDetail, ActionAuthorizeCodex, ActionAuthorizeClaude:
		return target == 0
	case ActionInteractionAccept, ActionInteractionDecline, ActionInteractionCancel,
		ActionOutboundConfirmDelivered, ActionOutboundRetryPossibleDuplicate,
		ActionCallbackEffectConfirmed, ActionCallbackEffectRetryPossibleDuplicate,
		ActionCallbackSendConfirmed, ActionCallbackSendRetryPossibleDuplicate, ActionInteractionOther,
		ActionAcceptedTurnAssumeCompleted, ActionAcceptedTurnRetryPossibleDuplicate, ActionAcceptedTurnCancel,
		ActionStatusRecoveryAssumeDelivered, ActionStatusRecoveryRetryPossibleDuplicate, ActionStatusRecoveryCancel, ActionArtifactRetry:
		return target == 0
	default:
		return false
	}
}

func authenticationTag(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)[:tagSize]
}

func parseCanonicalUUID(value string) ([16]byte, error) {
	var uuid [16]byte
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return uuid, ErrInvalidFields
	}
	n := 0
	for i := 0; i < len(value); {
		if value[i] == '-' {
			i++
			continue
		}
		hi, ok := hexNibble(value[i])
		if !ok || i+1 >= len(value) {
			return uuid, ErrInvalidFields
		}
		lo, ok := hexNibble(value[i+1])
		if !ok {
			return uuid, ErrInvalidFields
		}
		uuid[n] = hi<<4 | lo
		n++
		i += 2
	}
	if n != len(uuid) || formatUUID(uuid) != value {
		return [16]byte{}, ErrInvalidFields
	}
	return uuid, nil
}

func hexNibble(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	default:
		return 0, false
	}
}

func formatUUID(uuid [16]byte) string {
	result := make([]byte, 36)
	hex.Encode(result[0:8], uuid[0:4])
	result[8] = '-'
	hex.Encode(result[9:13], uuid[4:6])
	result[13] = '-'
	hex.Encode(result[14:18], uuid[6:8])
	result[18] = '-'
	hex.Encode(result[19:23], uuid[8:10])
	result[23] = '-'
	hex.Encode(result[24:36], uuid[10:16])
	return string(result)
}
