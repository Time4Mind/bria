package nodecontrol

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/security"
)

func TestClientTLSAllowsOnlyActiveOrLinkedRotationKey(t *testing.T) {
	now := time.Now()
	ca, caPEM, _, err := security.GenerateCA("cluster", now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	current, err := security.IssueNodeCertificate(ca, "cluster", "node", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	currentLeaf, err := security.ParseCertificate(current.CertificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := security.NodeCertificateFingerprint(currentLeaf)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	clientIdentity, err := security.IssueNodeCertificate(ca, "cluster", "client", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	clientCertificate, err := tls.X509KeyPair(
		clientIdentity.CertificatePEM, clientIdentity.PrivateKeyPEM,
	)
	if err != nil {
		t.Fatal(err)
	}
	config, err := clientTLSConfig(
		clientCertificate, roots, "cluster", "node", fingerprint,
	)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(certificatePEM []byte) error {
		certificate, parseErr := security.ParseCertificate(certificatePEM)
		if parseErr != nil {
			return parseErr
		}
		return config.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}})
	}
	if err := verify(current.CertificatePEM); err != nil {
		t.Fatalf("active key rejected: %v", err)
	}
	unlinked, err := security.IssueNodeCertificate(ca, "cluster", "node", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(unlinked.CertificatePEM); err == nil {
		t.Fatal("unlinked replacement key accepted")
	}
	newPublicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := security.IssueRotatedNodeCertificateForPublicKey(
		ca, "cluster", "node", newPublicKey, fingerprint, now, time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(rotated); err != nil {
		t.Fatalf("linked rotation key rejected: %v", err)
	}
}
