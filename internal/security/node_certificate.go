package security

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"time"
)

var previousNodeKeyFingerprintOID = []int{1, 3, 6, 1, 4, 1, 55555, 66, 1}

func IssueNodeCertificate(
	ca CertificateAuthority,
	clusterID string,
	nodeID string,
	now time.Time,
	validFor time.Duration,
) (NodeCredentials, error) {
	if ca.Certificate == nil || ca.Signer == nil {
		return NodeCredentials{}, errors.New("certificate authority is incomplete")
	}
	if err := validateIdentityPart("cluster id", clusterID); err != nil {
		return NodeCredentials{}, err
	}
	if err := validateIdentityPart("node id", nodeID); err != nil {
		return NodeCredentials{}, err
	}
	if validFor <= 0 {
		return NodeCredentials{}, errors.New("node certificate validity must be positive")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return NodeCredentials{}, fmt.Errorf("generate node key: %w", err)
	}
	certificatePEM, err := IssueNodeCertificateForPublicKey(
		ca, clusterID, nodeID, publicKey, now, validFor,
	)
	if err != nil {
		return NodeCredentials{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return NodeCredentials{}, fmt.Errorf("encode node private key: %w", err)
	}
	return NodeCredentials{
		CertificatePEM: certificatePEM,
		PrivateKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

func IssueNodeCertificateForPublicKey(
	ca CertificateAuthority,
	clusterID string,
	nodeID string,
	publicKey ed25519.PublicKey,
	now time.Time,
	validFor time.Duration,
) ([]byte, error) {
	return issueNodeCertificateForPublicKey(ca, clusterID, nodeID, publicKey, "", now, validFor)
}

// IssueRotatedNodeCertificateForPublicKey links the replacement key to the
// currently active key. Members accept it only while that key remains active.
func IssueRotatedNodeCertificateForPublicKey(
	ca CertificateAuthority,
	clusterID string,
	nodeID string,
	publicKey ed25519.PublicKey,
	previousFingerprint string,
	now time.Time,
	validFor time.Duration,
) ([]byte, error) {
	return issueNodeCertificateForPublicKey(
		ca, clusterID, nodeID, publicKey, previousFingerprint, now, validFor,
	)
}

func issueNodeCertificateForPublicKey(
	ca CertificateAuthority,
	clusterID string,
	nodeID string,
	publicKey ed25519.PublicKey,
	previousFingerprint string,
	now time.Time,
	validFor time.Duration,
) ([]byte, error) {
	if ca.Certificate == nil || ca.Signer == nil || len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("certificate authority or node public key is incomplete")
	}
	if err := validateIdentityPart("cluster id", clusterID); err != nil {
		return nil, err
	}
	if err := validateIdentityPart("node id", nodeID); err != nil {
		return nil, err
	}
	if validFor <= 0 {
		return nil, errors.New("node certificate validity must be positive")
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    now.UTC().Add(-5 * time.Minute),
		NotAfter:     now.UTC().Add(validFor),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
			x509.ExtKeyUsageServerAuth,
		},
		URIs: []*url.URL{nodeURI(clusterID, nodeID)},
	}
	if previousFingerprint != "" {
		fingerprint, decodeErr := hex.DecodeString(previousFingerprint)
		if decodeErr != nil || len(fingerprint) != sha256.Size ||
			hex.EncodeToString(fingerprint) != previousFingerprint {
			return nil, errors.New("previous node key fingerprint must be lowercase SHA-256")
		}
		template.ExtraExtensions = []pkix.Extension{{
			Id: previousNodeKeyFingerprintOID, Value: fingerprint,
		}}
	}
	der, err := x509.CreateCertificate(
		rand.Reader, template, ca.Certificate, publicKey, ca.Signer,
	)
	if err != nil {
		return nil, fmt.Errorf("issue node certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// NodeCertificateFingerprint returns the SHA-256 fingerprint also used by enrollment.
func NodeCertificateFingerprint(certificate *x509.Certificate) (string, error) {
	if certificate == nil {
		return "", errors.New("node certificate is required")
	}
	publicKey, ok := certificate.PublicKey.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return "", errors.New("node certificate key is not Ed25519")
	}
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:]), nil
}

func PreviousNodeCertificateFingerprint(certificate *x509.Certificate) (string, bool, error) {
	if certificate == nil {
		return "", false, errors.New("node certificate is required")
	}
	var value []byte
	for _, extension := range certificate.Extensions {
		if !extension.Id.Equal(previousNodeKeyFingerprintOID) {
			continue
		}
		if value != nil {
			return "", false, errors.New("duplicate previous fingerprint extension")
		}
		value = extension.Value
	}
	if value == nil {
		return "", false, nil
	}
	if len(value) != sha256.Size {
		return "", false, errors.New("invalid previous fingerprint extension")
	}
	return hex.EncodeToString(value), true, nil
}
