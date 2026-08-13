// Package enrollment implements the narrow pre-membership protocol used by a
// new Bria node. It exposes no cluster state beyond approved bootstrap data.
package enrollment

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

const (
	registerPath = "/v1/enrollment/register"
	statusPath   = "/v1/enrollment/status"
	maxPayload   = 32 << 10
)

type RegisterRequest struct {
	TokenID  string `json:"token_id"`
	Secret   string `json:"secret"`
	Contract string `json:"contract"`
}

type RegisterResponse struct {
	RequestID string    `json:"request_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type StatusRequest struct {
	RequestID string `json:"request_id"`
	Timestamp int64  `json:"timestamp"`
	Signature string `json:"signature"`
}

type Peer struct {
	NodeID         string `json:"node_id"`
	Name           string `json:"name"`
	RaftAddress    string `json:"raft_address"`
	ControlAddress string `json:"control_address"`
}

type ApprovedBundle struct {
	ClusterID         string `json:"cluster_id"`
	IssuerNodeID      string `json:"issuer_node_id"`
	EnrollmentAddress string `json:"enrollment_address"`
	Certificate       string `json:"certificate"`
	CACertificate     string `json:"ca_certificate"`
	CallbackKey       string `json:"callback_key"`
	Peers             []Peer `json:"peers"`
}

type StatusResponse struct {
	Status domain.EnrollmentStatus `json:"status"`
	Bundle *ApprovedBundle         `json:"bundle,omitempty"`
}

func NewStatusRequest(requestID string, privateKey ed25519.PrivateKey, now time.Time) (StatusRequest, error) {
	if requestID == "" || len(privateKey) != ed25519.PrivateKeySize {
		return StatusRequest{}, errors.New("enrollment status identity is incomplete")
	}
	timestamp := now.UTC().Unix()
	signature := ed25519.Sign(privateKey, statusProof(requestID, timestamp))
	return StatusRequest{
		RequestID: requestID, Timestamp: timestamp,
		Signature: base64.RawURLEncoding.EncodeToString(signature),
	}, nil
}

func VerifyStatusRequest(request StatusRequest, publicKey ed25519.PublicKey, now time.Time) error {
	if len(publicKey) != ed25519.PublicKeySize || request.RequestID == "" ||
		request.Timestamp < now.Add(-2*time.Minute).Unix() ||
		request.Timestamp > now.Add(2*time.Minute).Unix() {
		return errors.New("invalid enrollment status proof")
	}
	signature, err := base64.RawURLEncoding.DecodeString(request.Signature)
	if err != nil || !ed25519.Verify(publicKey,
		statusProof(request.RequestID, request.Timestamp), signature) {
		return errors.New("invalid enrollment status proof")
	}
	return nil
}

func statusProof(requestID string, timestamp int64) []byte {
	return []byte("bria-enrollment-status-v1\x00" + requestID + "\x00" + strconv.FormatInt(timestamp, 10))
}
