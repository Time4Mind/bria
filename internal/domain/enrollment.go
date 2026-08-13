package domain

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type EnrollmentStatus string

const (
	EnrollmentPending  EnrollmentStatus = "pending"
	EnrollmentApproved EnrollmentStatus = "approved"
	EnrollmentRejected EnrollmentStatus = "rejected"
)

type EnrollmentInvite struct {
	ID         string    `json:"id"`
	SecretHash string    `json:"secret_hash"`
	ExpiresAt  time.Time `json:"expires_at"`
	UsedAt     time.Time `json:"used_at,omitempty"`
}

type EnrollmentRequest struct {
	ID          string           `json:"id"`
	InviteID    string           `json:"invite_id,omitempty"`
	NodeID      NodeID           `json:"node_id"`
	Name        string           `json:"name"`
	Network     NodeNetwork      `json:"network"`
	OS          string           `json:"os,omitempty"`
	Arch        string           `json:"arch,omitempty"`
	PublicKey   string           `json:"public_key"`
	Fingerprint string           `json:"fingerprint"`
	Status      EnrollmentStatus `json:"status"`
	RequestedAt time.Time        `json:"requested_at"`
	ExpiresAt   time.Time        `json:"expires_at"`
	DecidedAt   time.Time        `json:"decided_at,omitempty"`
	NotifiedAt  time.Time        `json:"notified_at,omitempty"`
}

type NodeTombstone struct {
	NodeID      NodeID    `json:"node_id"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	DeletedAt   time.Time `json:"deleted_at"`
}

func HashEnrollmentSecret(secret string) string {
	hash := sha256.Sum256([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func (invite EnrollmentInvite) ValidateSecret(secret string, now time.Time) error {
	if !invite.UsedAt.IsZero() {
		return errors.New("enrollment invitation has already been used")
	}
	if !now.UTC().Before(invite.ExpiresAt) {
		return errors.New("enrollment invitation has expired")
	}
	want, err := base64.RawURLEncoding.DecodeString(invite.SecretHash)
	if err != nil || len(want) != sha256.Size {
		return errors.New("enrollment invitation hash is invalid")
	}
	got := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(want, got[:]) != 1 {
		return errors.New("invalid enrollment invitation")
	}
	return nil
}

func (request EnrollmentRequest) Validate() error {
	if err := validateIdentifier("enrollment request id", request.ID); err != nil {
		return err
	}
	if err := validateIdentifier("node id", string(request.NodeID)); err != nil {
		return err
	}
	if strings.TrimSpace(request.Name) == "" || len(request.Name) > 64 ||
		strings.ContainsAny(request.Name, "\r\n\t") {
		return errors.New("node name must contain 1 to 64 characters")
	}
	if err := validateEnrollmentAddress("raft", request.Network.RaftAddress); err != nil {
		return err
	}
	if err := validateEnrollmentAddress("control", request.Network.ControlAddress); err != nil {
		return err
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(request.PublicKey)
	fingerprint, fingerprintErr := hex.DecodeString(request.Fingerprint)
	digest := sha256.Sum256(publicKey)
	if err != nil || len(publicKey) != 32 || fingerprintErr != nil ||
		len(fingerprint) != sha256.Size || subtle.ConstantTimeCompare(fingerprint, digest[:]) != 1 {
		return errors.New("node public identity is required")
	}
	if request.RequestedAt.IsZero() || !request.ExpiresAt.After(request.RequestedAt) {
		return fmt.Errorf("%w: enrollment request lifetime is invalid", ErrInvalidState)
	}
	return nil
}

func validateEnrollmentAddress(label, address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(address) != address || strings.TrimSpace(host) == "" ||
		strings.TrimSpace(host) != host || host == "*" {
		return fmt.Errorf("node %s address must be a routable host:port", label)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return fmt.Errorf("node %s address must be a routable host:port", label)
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return fmt.Errorf("node %s address port must be between 1 and 65535", label)
	}
	return nil
}
