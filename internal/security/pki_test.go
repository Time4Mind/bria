package security_test

import (
	"crypto/x509"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/security"
)

func TestNodeCertificateBindsStableIdentity(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	ca, caPEM, keyPEM, err := security.GenerateCA("cluster-a", now, 10*365*24*time.Hour)
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	parsedCA, err := security.ParseCA(caPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	if !ca.Certificate.Equal(parsedCA.Certificate) {
		t.Fatal("parsed CA differs")
	}
	credentials, err := security.IssueNodeCertificate(parsedCA, "cluster-a", "node-1", now, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("issue certificate: %v", err)
	}
	certificate, err := security.ParseCertificate(credentials.CertificatePEM)
	if err != nil {
		t.Fatalf("parse node certificate: %v", err)
	}
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		t.Fatalf("certificate pool: %v", err)
	}
	if err := security.VerifyNodeCertificate(certificate, roots, "cluster-a", "node-1", now, x509.ExtKeyUsageServerAuth); err != nil {
		t.Fatalf("verify correct node: %v", err)
	}
	if err := security.VerifyNodeCertificate(certificate, roots, "cluster-a", "node-2", now, x509.ExtKeyUsageServerAuth); err == nil {
		t.Fatal("wrong node identity accepted")
	}
}

func TestEnrollmentTokenIsSingleUseAndExpires(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	token, secret, err := security.NewEnrollmentToken("join-1", "Office", now, time.Minute)
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	if err := token.Validate("wrong", now); err == nil {
		t.Fatal("wrong enrollment secret accepted")
	}
	if err := token.Consume(secret, now); err != nil {
		t.Fatalf("consume token: %v", err)
	}
	if err := token.Validate(secret, now); err == nil {
		t.Fatal("used token accepted")
	}
	expired, expiredSecret, err := security.NewEnrollmentToken("join-2", "Home", now, time.Second)
	if err != nil {
		t.Fatalf("new expiring token: %v", err)
	}
	if err := expired.Validate(expiredSecret, now.Add(time.Second)); err == nil {
		t.Fatal("expired token accepted")
	}
}
