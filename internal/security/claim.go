package security

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const claimPrefix = "bria-claim1."

type EnrollmentClaim struct {
	Version       int       `json:"v"`
	ClusterID     string    `json:"cluster"`
	IssuerNodeID  string    `json:"issuer"`
	Endpoint      string    `json:"endpoint"`
	RequestID     string    `json:"request_id"`
	CACertificate string    `json:"ca"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func EncodeEnrollmentClaim(claim EnrollmentClaim) (string, error) {
	if err := claim.Validate(time.Time{}); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(claim)
	if err != nil {
		return "", err
	}
	return claimPrefix + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func DecodeEnrollmentClaim(value string, now time.Time) (EnrollmentClaim, error) {
	if !strings.HasPrefix(value, claimPrefix) || len(value) > 8192 {
		return EnrollmentClaim{}, errors.New("invalid Bria enrollment claim")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, claimPrefix))
	if err != nil {
		return EnrollmentClaim{}, errors.New("invalid Bria enrollment claim encoding")
	}
	var claim EnrollmentClaim
	if json.Unmarshal(raw, &claim) != nil || claim.Validate(now) != nil {
		return EnrollmentClaim{}, errors.New("invalid or expired Bria enrollment claim")
	}
	return claim, nil
}

func (claim EnrollmentClaim) Validate(now time.Time) error {
	if claim.Version != 1 || claim.ClusterID == "" || claim.IssuerNodeID == "" ||
		claim.Endpoint == "" || claim.RequestID == "" || claim.CACertificate == "" ||
		claim.ExpiresAt.IsZero() {
		return errors.New("enrollment claim is incomplete")
	}
	if !now.IsZero() && !now.UTC().Before(claim.ExpiresAt) {
		return errors.New("enrollment claim has expired")
	}
	return nil
}
