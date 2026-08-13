package clusterbackup

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	Version          = 2
	MaxFileBytes     = 32 << 20
	MaxSnapshotBytes = 24 << 20
)

type Envelope struct {
	Version        int       `json:"version"`
	ClusterID      string    `json:"cluster_id"`
	SourceNodeID   string    `json:"source_node_id"`
	CreatedAt      time.Time `json:"created_at"`
	Snapshot       []byte    `json:"snapshot"`
	SnapshotSHA256 string    `json:"snapshot_sha256"`
	CertificatePEM []byte    `json:"certificate_pem"`
	Signature      []byte    `json:"signature"`
}

func New(clusterID string, sourceNodeID string, snapshot []byte, at time.Time) (Envelope, error) {
	if !safeIdentity(clusterID) || !safeIdentity(sourceNodeID) || at.IsZero() {
		return Envelope{}, errors.New("backup identity and creation time are required")
	}
	if len(snapshot) == 0 || len(snapshot) > MaxSnapshotBytes {
		return Envelope{}, errors.New("backup snapshot size is invalid")
	}
	digest := sha256.Sum256(snapshot)
	return Envelope{
		Version: Version, ClusterID: clusterID, SourceNodeID: sourceNodeID,
		CreatedAt: at.UTC(), Snapshot: append([]byte(nil), snapshot...),
		SnapshotSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func (e Envelope) Validate() error {
	if err := e.validateCore(); err != nil {
		return err
	}
	if len(e.CertificatePEM) == 0 || len(e.CertificatePEM) > 64<<10 ||
		len(e.Signature) != ed25519.SignatureSize {
		return errors.New("backup signature is incomplete")
	}
	return nil
}

func (e Envelope) validateCore() error {
	if e.Version != Version {
		return fmt.Errorf("unsupported backup version: %d", e.Version)
	}
	if !safeIdentity(e.ClusterID) || !safeIdentity(e.SourceNodeID) || e.CreatedAt.IsZero() ||
		len(e.Snapshot) == 0 || len(e.Snapshot) > MaxSnapshotBytes {
		return errors.New("backup envelope is incomplete")
	}
	digest := sha256.Sum256(e.Snapshot)
	if e.SnapshotSHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("backup snapshot checksum mismatch")
	}
	return nil
}

func (e *Envelope) Sign(certificatePEM []byte, privateKey ed25519.PrivateKey) error {
	if e == nil || len(certificatePEM) == 0 || len(certificatePEM) > 64<<10 ||
		len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("backup signing identity is incomplete")
	}
	if err := e.validateCore(); err != nil {
		return err
	}
	e.CertificatePEM = append([]byte(nil), certificatePEM...)
	e.Signature = nil
	payload, err := e.signingPayload()
	if err != nil {
		return err
	}
	e.Signature = ed25519.Sign(privateKey, payload)
	return nil
}

func (e Envelope) VerifySignature(publicKey ed25519.PublicKey) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("backup verification key is invalid")
	}
	payload, err := e.signingPayload()
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, payload, e.Signature) {
		return errors.New("backup signature is invalid")
	}
	return nil
}

func (e Envelope) signingPayload() ([]byte, error) {
	e.Signature = nil
	return json.Marshal(e)
}

func safeIdentity(value string) bool {
	if strings.TrimSpace(value) == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char <= 0x20 || char == '/' || char == '\\' {
			return false
		}
	}
	return true
}

func (e Envelope) Marshal() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode backup: %w", err)
	}
	if len(encoded) > MaxFileBytes {
		return nil, errors.New("encoded backup exceeds size limit")
	}
	return append(encoded, '\n'), nil
}

func Parse(data []byte) (Envelope, error) {
	if len(data) == 0 || len(data) > MaxFileBytes {
		return Envelope{}, errors.New("backup file size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("decode backup: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Envelope{}, errors.New("backup contains trailing JSON values")
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}
