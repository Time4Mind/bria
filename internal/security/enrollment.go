package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"
)

const enrollmentTokenBytes = 32

type EnrollmentToken struct {
	ID        string    `json:"id"`
	NodeName  string    `json:"node_name"`
	Hash      [32]byte  `json:"hash"`
	ExpiresAt time.Time `json:"expires_at"`
	UsedAt    time.Time `json:"used_at,omitempty"`
}

func NewEnrollmentToken(id, nodeName string, now time.Time, ttl time.Duration) (
	EnrollmentToken,
	string,
	error,
) {
	if err := validateIdentityPart("token id", id); err != nil {
		return EnrollmentToken{}, "", err
	}
	if nodeName == "" {
		return EnrollmentToken{}, "", errors.New("node name is required")
	}
	if ttl <= 0 {
		return EnrollmentToken{}, "", errors.New("token TTL must be positive")
	}
	raw := make([]byte, enrollmentTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return EnrollmentToken{}, "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	return EnrollmentToken{
		ID:        id,
		NodeName:  nodeName,
		Hash:      sha256.Sum256([]byte(secret)),
		ExpiresAt: now.UTC().Add(ttl),
	}, secret, nil
}

func (t EnrollmentToken) Validate(secret string, now time.Time) error {
	if !t.UsedAt.IsZero() {
		return errors.New("enrollment token has already been used")
	}
	if !now.UTC().Before(t.ExpiresAt) {
		return errors.New("enrollment token has expired")
	}
	candidate := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(t.Hash[:], candidate[:]) != 1 {
		return errors.New("invalid enrollment token")
	}
	return nil
}

func (t *EnrollmentToken) Consume(secret string, now time.Time) error {
	if err := t.Validate(secret, now); err != nil {
		return err
	}
	t.UsedAt = now.UTC()
	return nil
}
