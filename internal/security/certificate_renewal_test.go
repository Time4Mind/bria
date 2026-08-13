package security_test

import (
	"crypto/ed25519"
	"crypto/x509"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/security"
)

func TestCertificateRenewalBindsOldAndNewNodeKeys(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	ca, caPEM, _, err := security.GenerateCA("cluster-a", now, 10*365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	current, err := security.IssueNodeCertificate(ca, "cluster-a", "node-1", now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	currentKey, err := security.ParseEd25519PrivateKey(current.PrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	request, newKey, err := security.NewCertificateRenewalRequest(
		"cluster-a", "node-1", current.CertificatePEM, currentKey, now, 30*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	response, err := security.IssueCertificateRenewal(ca, request, roots, now, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := security.VerifyCertificateRenewalResponse(response, request, newKey, roots, now); err != nil {
		t.Fatalf("verify renewal response: %v", err)
	}
	certificate, err := security.ParseCertificate(response.CertificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	if err := security.VerifyNodeCertificate(
		certificate, roots, "cluster-a", "node-1", now, x509.ExtKeyUsageServerAuth,
	); err != nil {
		t.Fatalf("verify renewed server identity: %v", err)
	}
	currentCertificate, err := security.ParseCertificate(current.CertificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	wantPrevious, err := security.NodeCertificateFingerprint(currentCertificate)
	if err != nil {
		t.Fatal(err)
	}
	gotPrevious, present, err := security.PreviousNodeCertificateFingerprint(certificate)
	if err != nil || !present || gotPrevious != wantPrevious {
		t.Fatalf("rotation predecessor=%q present=%v err=%v", gotPrevious, present, err)
	}
}

func TestCertificateRenewalRejectsTamperingAndWrongPrivateKey(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	ca, caPEM, _, err := security.GenerateCA("cluster-a", now, 10*365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	current, err := security.IssueNodeCertificate(ca, "cluster-a", "node-1", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	currentKey, _ := security.ParseEd25519PrivateKey(current.PrivateKeyPEM)
	request, newKey, err := security.NewCertificateRenewalRequest(
		"cluster-a", "node-1", current.CertificatePEM, currentKey, now, 30*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	roots, _ := security.CertificatePool(caPEM)
	tampered := request
	tampered.NodeID = "node-2"
	if _, err := security.IssueCertificateRenewal(ca, tampered, roots, now, time.Hour); err == nil {
		t.Fatal("tampered node identity was accepted")
	}
	tampered = request
	tampered.ExpiresAt = tampered.CreatedAt.Add(-time.Second)
	if _, err := security.IssueCertificateRenewal(ca, tampered, roots, now, time.Hour); err == nil {
		t.Fatal("negative renewal validity was accepted")
	}
	response, err := security.IssueCertificateRenewal(ca, request, roots, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, wrongKey, _ := ed25519.GenerateKey(nil)
	if err := security.VerifyCertificateRenewalResponse(
		response, request, wrongKey, roots, now,
	); err == nil {
		t.Fatal("certificate was accepted with wrong private key")
	}
	response.NodeID = "node-2"
	if err := security.VerifyCertificateRenewalResponse(
		response, request, newKey, roots, now,
	); err == nil {
		t.Fatal("mismatched renewal response was accepted")
	}
}
