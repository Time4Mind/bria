package security

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

const nodeContractPrefix = "bria-node1."

type NodeContract struct {
	Version   int                `json:"v"`
	RequestID string             `json:"request_id"`
	NodeID    domain.NodeID      `json:"node_id"`
	Name      string             `json:"name"`
	Network   domain.NodeNetwork `json:"network"`
	OS        string             `json:"os"`
	Arch      string             `json:"arch"`
	PublicKey string             `json:"public_key"`
	ExpiresAt time.Time          `json:"expires_at"`
	Signature string             `json:"signature"`
}

func SignNodeContract(contract NodeContract, privateKey ed25519.PrivateKey) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", errors.New("invalid node contract private key")
	}
	contract.Version = 1
	publicKey := privateKey.Public().(ed25519.PublicKey)
	contract.PublicKey = base64.RawURLEncoding.EncodeToString(publicKey)
	contract.Signature = ""
	payload, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	contract.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	encoded, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	return nodeContractPrefix + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func DecodeNodeContract(value string, now time.Time) (NodeContract, error) {
	if !strings.HasPrefix(value, nodeContractPrefix) || len(value) > 8192 {
		return NodeContract{}, errors.New("invalid Bria node contract")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, nodeContractPrefix))
	if err != nil {
		return NodeContract{}, errors.New("invalid Bria node contract encoding")
	}
	var contract NodeContract
	if err := json.Unmarshal(raw, &contract); err != nil {
		return NodeContract{}, errors.New("invalid Bria node contract payload")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(contract.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || contract.Signature == "" {
		return NodeContract{}, errors.New("invalid Bria node contract identity")
	}
	signature, err := base64.RawURLEncoding.DecodeString(contract.Signature)
	if err != nil {
		return NodeContract{}, errors.New("invalid Bria node contract signature")
	}
	contract.Signature = ""
	payload, err := json.Marshal(contract)
	if err != nil || !ed25519.Verify(publicKey, payload, signature) {
		return NodeContract{}, errors.New("invalid Bria node contract signature")
	}
	contract.Signature = base64.RawURLEncoding.EncodeToString(signature)
	if contract.Version != 1 || contract.RequestID == "" || contract.NodeID == "" ||
		contract.Name == "" || !now.UTC().Before(contract.ExpiresAt) {
		return NodeContract{}, errors.New("invalid or expired Bria node contract")
	}
	return contract, nil
}

func (contract NodeContract) EnrollmentRequest(at time.Time) domain.EnrollmentRequest {
	publicKey, _ := base64.RawURLEncoding.DecodeString(contract.PublicKey)
	fingerprint := sha256.Sum256(publicKey)
	return domain.EnrollmentRequest{
		ID: contract.RequestID, NodeID: contract.NodeID, Name: contract.Name,
		Network: contract.Network, OS: contract.OS, Arch: contract.Arch,
		PublicKey: contract.PublicKey, Fingerprint: hex.EncodeToString(fingerprint[:]),
		RequestedAt: at.UTC(), ExpiresAt: at.UTC().Add(24 * time.Hour),
	}
}
