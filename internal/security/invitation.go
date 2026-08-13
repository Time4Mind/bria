package security

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const invitationPrefix = "bria1."

type ClusterInvitation struct {
	Version       int       `json:"v"`
	ClusterID     string    `json:"cluster"`
	IssuerNodeID  string    `json:"issuer"`
	Endpoint      string    `json:"endpoint"`
	TokenID       string    `json:"token"`
	Secret        string    `json:"secret"`
	CACertificate string    `json:"ca"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func EncodeClusterInvitation(invitation ClusterInvitation) (string, error) {
	if err := invitation.Validate(time.Time{}); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(invitation)
	if err != nil {
		return "", err
	}
	return invitationPrefix + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func DecodeClusterInvitation(value string, now time.Time) (ClusterInvitation, error) {
	if !strings.HasPrefix(value, invitationPrefix) || len(value) > 8192 {
		return ClusterInvitation{}, errors.New("invalid Bria cluster invitation")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, invitationPrefix))
	if err != nil {
		return ClusterInvitation{}, errors.New("invalid Bria cluster invitation encoding")
	}
	var invitation ClusterInvitation
	decoderErr := json.Unmarshal(raw, &invitation)
	if decoderErr != nil || invitation.Validate(now) != nil {
		return ClusterInvitation{}, errors.New("invalid or expired Bria cluster invitation")
	}
	return invitation, nil
}

func (invitation ClusterInvitation) Validate(now time.Time) error {
	if invitation.Version != 1 || invitation.ClusterID == "" || invitation.IssuerNodeID == "" ||
		invitation.Endpoint == "" || invitation.TokenID == "" || invitation.Secret == "" ||
		invitation.CACertificate == "" || invitation.ExpiresAt.IsZero() {
		return errors.New("cluster invitation is incomplete")
	}
	if !now.IsZero() && !now.UTC().Before(invitation.ExpiresAt) {
		return errors.New("cluster invitation has expired")
	}
	return nil
}
