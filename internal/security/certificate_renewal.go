package security

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

const certificateRenewalVersion = 1

type CertificateRenewalRequest struct {
	Version            int       `json:"version"`
	RequestID          string    `json:"request_id"`
	ClusterID          string    `json:"cluster_id"`
	NodeID             string    `json:"node_id"`
	CreatedAt          time.Time `json:"created_at"`
	ExpiresAt          time.Time `json:"expires_at"`
	CurrentCertificate []byte    `json:"current_certificate_pem"`
	NewPublicKey       string    `json:"new_public_key"`
	Signature          string    `json:"signature"`
}

type CertificateRenewalResponse struct {
	Version        int    `json:"version"`
	RequestID      string `json:"request_id"`
	ClusterID      string `json:"cluster_id"`
	NodeID         string `json:"node_id"`
	CertificatePEM []byte `json:"certificate_pem"`
}

func NewCertificateRenewalRequest(
	clusterID string,
	nodeID string,
	currentCertificate []byte,
	currentKey ed25519.PrivateKey,
	now time.Time,
	validFor time.Duration,
) (CertificateRenewalRequest, ed25519.PrivateKey, error) {
	if validFor <= 0 || validFor > time.Hour {
		return CertificateRenewalRequest{}, nil, errors.New("renewal request validity must be within one hour")
	}
	certificate, err := ParseCertificate(currentCertificate)
	if err != nil {
		return CertificateRenewalRequest{}, nil, err
	}
	certificateNodeID, err := NodeIDFromCertificate(certificate, clusterID)
	if err != nil || certificateNodeID != nodeID {
		return CertificateRenewalRequest{}, nil, errors.New("current certificate identity does not match node")
	}
	publicKey, ok := certificate.PublicKey.(ed25519.PublicKey)
	if !ok || len(currentKey) != ed25519.PrivateKeySize || !publicKey.Equal(currentKey.Public()) {
		return CertificateRenewalRequest{}, nil, errors.New("current node certificate and private key do not match")
	}
	newPublicKey, newPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return CertificateRenewalRequest{}, nil, fmt.Errorf("generate renewal key: %w", err)
	}
	requestID, err := randomRequestID()
	if err != nil {
		return CertificateRenewalRequest{}, nil, err
	}
	request := CertificateRenewalRequest{
		Version: certificateRenewalVersion, RequestID: requestID,
		ClusterID: clusterID, NodeID: nodeID, CreatedAt: now.UTC(),
		ExpiresAt: now.UTC().Add(validFor), CurrentCertificate: currentCertificate,
		NewPublicKey: base64.RawURLEncoding.EncodeToString(newPublicKey),
	}
	payload, err := renewalRequestPayload(request)
	if err != nil {
		return CertificateRenewalRequest{}, nil, err
	}
	request.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(currentKey, payload))
	return request, newPrivateKey, nil
}

func VerifyCertificateRenewalRequest(
	request CertificateRenewalRequest,
	roots *x509.CertPool,
	now time.Time,
) (ed25519.PublicKey, error) {
	if request.Version != certificateRenewalVersion || request.RequestID == "" ||
		len(request.RequestID) > 128 || request.Signature == "" {
		return nil, errors.New("invalid certificate renewal request")
	}
	if err := validateIdentityPart("cluster id", request.ClusterID); err != nil {
		return nil, err
	}
	if err := validateIdentityPart("node id", request.NodeID); err != nil {
		return nil, err
	}
	current := now.UTC()
	if !request.ExpiresAt.After(request.CreatedAt) ||
		request.CreatedAt.After(current.Add(5*time.Minute)) || !current.Before(request.ExpiresAt) ||
		request.ExpiresAt.After(request.CreatedAt.Add(time.Hour)) {
		return nil, errors.New("invalid or expired certificate renewal request")
	}
	certificate, err := ParseCertificate(request.CurrentCertificate)
	if err != nil {
		return nil, err
	}
	if err := VerifyNodeCertificate(
		certificate, roots, request.ClusterID, request.NodeID, current, x509.ExtKeyUsageClientAuth,
	); err != nil {
		return nil, err
	}
	currentPublicKey, ok := certificate.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("current node certificate key is not Ed25519")
	}
	signature, err := base64.RawURLEncoding.DecodeString(request.Signature)
	if err != nil {
		return nil, errors.New("invalid certificate renewal signature")
	}
	payload, err := renewalRequestPayload(request)
	if err != nil || !ed25519.Verify(currentPublicKey, payload, signature) {
		return nil, errors.New("invalid certificate renewal signature")
	}
	newPublicKey, err := base64.RawURLEncoding.DecodeString(request.NewPublicKey)
	if err != nil || len(newPublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("invalid certificate renewal public key")
	}
	return ed25519.PublicKey(newPublicKey), nil
}

func IssueCertificateRenewal(
	ca CertificateAuthority,
	request CertificateRenewalRequest,
	roots *x509.CertPool,
	now time.Time,
	validFor time.Duration,
) (CertificateRenewalResponse, error) {
	publicKey, err := VerifyCertificateRenewalRequest(request, roots, now)
	if err != nil {
		return CertificateRenewalResponse{}, err
	}
	currentCertificate, err := ParseCertificate(request.CurrentCertificate)
	if err != nil {
		return CertificateRenewalResponse{}, err
	}
	previousFingerprint, err := NodeCertificateFingerprint(currentCertificate)
	if err != nil {
		return CertificateRenewalResponse{}, err
	}
	certificate, err := IssueRotatedNodeCertificateForPublicKey(
		ca, request.ClusterID, request.NodeID, publicKey, previousFingerprint, now, validFor,
	)
	if err != nil {
		return CertificateRenewalResponse{}, err
	}
	return CertificateRenewalResponse{
		Version: certificateRenewalVersion, RequestID: request.RequestID,
		ClusterID: request.ClusterID, NodeID: request.NodeID, CertificatePEM: certificate,
	}, nil
}

func VerifyCertificateRenewalResponse(
	response CertificateRenewalResponse,
	request CertificateRenewalRequest,
	newPrivateKey ed25519.PrivateKey,
	roots *x509.CertPool,
	now time.Time,
) error {
	if response.Version != certificateRenewalVersion || response.RequestID != request.RequestID ||
		response.ClusterID != request.ClusterID || response.NodeID != request.NodeID {
		return errors.New("certificate renewal response does not match request")
	}
	certificate, err := ParseCertificate(response.CertificatePEM)
	if err != nil {
		return err
	}
	if err := VerifyNodeCertificate(
		certificate, roots, request.ClusterID, request.NodeID, now, x509.ExtKeyUsageClientAuth,
	); err != nil {
		return err
	}
	publicKey, ok := certificate.PublicKey.(ed25519.PublicKey)
	if !ok || len(newPrivateKey) != ed25519.PrivateKeySize || !publicKey.Equal(newPrivateKey.Public()) {
		return errors.New("renewed certificate and private key do not match")
	}
	current, err := ParseCertificate(request.CurrentCertificate)
	if err != nil {
		return err
	}
	wantPrevious, err := NodeCertificateFingerprint(current)
	if err != nil {
		return err
	}
	previous, present, err := PreviousNodeCertificateFingerprint(certificate)
	if err != nil || !present || previous != wantPrevious {
		return errors.New("renewed certificate is not linked to the active node key")
	}
	return nil
}

func MarshalEd25519PrivateKey(privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 private key")
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("encode Ed25519 private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), nil
}

func ParseEd25519PrivateKey(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("invalid PKCS#8 private key PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse Ed25519 private key: %w", err)
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not Ed25519")
	}
	return privateKey, nil
}

func renewalRequestPayload(request CertificateRenewalRequest) ([]byte, error) {
	request.Signature = ""
	return json.Marshal(request)
}

func randomRequestID() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate renewal request id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
