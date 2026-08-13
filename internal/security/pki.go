// Package security implements Bria node identity and enrollment primitives.
package security

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
)

const trustDomain = "bria"

type CertificateAuthority struct {
	Certificate *x509.Certificate
	Signer      crypto.Signer
}

type NodeCredentials struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
}

func GenerateCA(clusterID string, now time.Time, validFor time.Duration) (
	CertificateAuthority,
	[]byte,
	[]byte,
	error,
) {
	if err := validateIdentityPart("cluster id", clusterID); err != nil {
		return CertificateAuthority{}, nil, nil, err
	}
	if validFor <= 0 {
		return CertificateAuthority{}, nil, nil, errors.New("CA validity must be positive")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return CertificateAuthority{}, nil, nil, fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return CertificateAuthority{}, nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Bria cluster " + clusterID},
		NotBefore:             now.UTC().Add(-5 * time.Minute),
		NotAfter:              now.UTC().Add(validFor),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		return CertificateAuthority{}, nil, nil, fmt.Errorf("create CA certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return CertificateAuthority{}, nil, nil, fmt.Errorf("parse generated CA certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return CertificateAuthority{}, nil, nil, fmt.Errorf("encode CA private key: %w", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return CertificateAuthority{Certificate: certificate, Signer: privateKey}, certificatePEM, privateKeyPEM, nil
}

func ParseCA(certificatePEM, privateKeyPEM []byte) (CertificateAuthority, error) {
	certificate, err := parseCertificate(certificatePEM)
	if err != nil {
		return CertificateAuthority{}, err
	}
	if !certificate.IsCA {
		return CertificateAuthority{}, errors.New("certificate is not a CA")
	}
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		return CertificateAuthority{}, errors.New("invalid PKCS#8 private key PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return CertificateAuthority{}, fmt.Errorf("parse CA private key: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return CertificateAuthority{}, errors.New("CA private key is not a signer")
	}
	if !certificate.PublicKey.(ed25519.PublicKey).Equal(signer.Public()) {
		return CertificateAuthority{}, errors.New("CA certificate and private key do not match")
	}
	return CertificateAuthority{Certificate: certificate, Signer: signer}, nil
}

func VerifyNodeCertificate(
	certificate *x509.Certificate,
	roots *x509.CertPool,
	clusterID string,
	expectedNodeID string,
	now time.Time,
	usage x509.ExtKeyUsage,
) error {
	if certificate == nil || roots == nil {
		return errors.New("certificate and root pool are required")
	}
	if err := validateIdentityPart("cluster id", clusterID); err != nil {
		return err
	}
	if err := validateIdentityPart("node id", expectedNodeID); err != nil {
		return err
	}
	if _, err := certificate.Verify(x509.VerifyOptions{
		Roots:       roots,
		CurrentTime: now.UTC(),
		KeyUsages:   []x509.ExtKeyUsage{usage},
	}); err != nil {
		return fmt.Errorf("verify node certificate chain: %w", err)
	}
	want := nodeURI(clusterID, expectedNodeID).String()
	for _, identity := range certificate.URIs {
		if identity.String() == want {
			return nil
		}
	}
	return fmt.Errorf("node certificate identity mismatch: expected %s", want)
}

func NodeIDFromCertificate(certificate *x509.Certificate, clusterID string) (string, error) {
	if certificate == nil {
		return "", errors.New("certificate is required")
	}
	prefix := "/cluster/" + clusterID + "/node/"
	for _, identity := range certificate.URIs {
		if identity.Scheme != "spiffe" ||
			identity.Host != trustDomain ||
			!strings.HasPrefix(identity.Path, prefix) {
			continue
		}
		nodeID := strings.TrimPrefix(identity.Path, prefix)
		if strings.Contains(nodeID, "/") {
			continue
		}
		if err := validateIdentityPart("node id", nodeID); err != nil {
			continue
		}
		return nodeID, nil
	}
	return "", errors.New("certificate has no valid Bria node identity")
}

func CertificatePool(certificatePEM []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certificatePEM) {
		return nil, errors.New("CA certificate PEM contains no certificate")
	}
	return pool, nil
}

func ParseCertificate(certificatePEM []byte) (*x509.Certificate, error) {
	return parseCertificate(certificatePEM)
}

func nodeURI(clusterID, nodeID string) *url.URL {
	return nodeURIForDomain(trustDomain, clusterID, nodeID)
}

func nodeURIForDomain(domain, clusterID, nodeID string) *url.URL {
	return &url.URL{
		Scheme: "spiffe",
		Host:   domain,
		Path:   "/cluster/" + clusterID + "/node/" + nodeID,
	}
}

func validateIdentityPart(label, value string) error {
	if strings.TrimSpace(value) == "" || len(value) > 128 {
		return fmt.Errorf("%s must contain 1 to 128 characters", label)
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.') {
			return fmt.Errorf("%s contains an unsafe character", label)
		}
	}
	return nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	return serial, nil
}

func parseCertificate(certificatePEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("invalid certificate PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return certificate, nil
}
