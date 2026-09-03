package nodelink_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"bria/internal/nodelink"
)

type pipeDialer struct{ connection net.Conn }

func (dialer pipeDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return dialer.connection, nil
}

func TestExecutorInitiatesMutuallyAuthenticatedPinnedTLSChannel(t *testing.T) {
	coordinatorCertificate, coordinatorLeaf := testCertificate(t, "coordinator")
	executorCertificate, executorLeaf := testCertificate(t, "executor")
	serverRaw, clientRaw := net.Pipe()
	defer serverRaw.Close()
	defer clientRaw.Close()

	serverResult := make(chan struct {
		channel *nodelink.SecureChannel
		err     error
	}, 1)
	go func() {
		channel, err := nodelink.AcceptExecutor(context.Background(), serverRaw, nodelink.TLSIdentity{
			LocalComputerID: "coordinator-1", PeerComputerID: "executor-1", ExecutorComputerID: "executor-1",
			Certificate: coordinatorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(executorLeaf),
		}, 1024)
		serverResult <- struct {
			channel *nodelink.SecureChannel
			err     error
		}{channel, err}
	}()
	client, err := nodelink.DialCoordinator(context.Background(), pipeDialer{clientRaw}, "pipe", nodelink.TLSIdentity{
		LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1",
		Certificate: executorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(coordinatorLeaf),
	}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	server := <-serverResult
	if server.err != nil {
		t.Fatal(server.err)
	}
	if identity := server.channel.Identity(); !identity.MutuallyAuthenticated || identity.PeerComputerID != "executor-1" || identity.ExecutorComputerID != "executor-1" {
		t.Fatalf("server channel identity = %#v", identity)
	}

	want := commandEnvelope()
	writeDone := make(chan error, 1)
	go func() { writeDone <- client.WriteEnvelope(context.Background(), want) }()
	got, err := server.channel.ReadEnvelope(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if got.OperationID != want.OperationID || string(got.Payload) != string(want.Payload) {
		t.Fatalf("received envelope = %#v, want %#v", got, want)
	}
}

func TestPinnedTLSRejectsDifferentCoordinatorCertificate(t *testing.T) {
	_, coordinatorLeaf := testCertificate(t, "coordinator")
	_, wrongCoordinatorLeaf := testCertificate(t, "wrong-coordinator")
	err := nodelink.VerifyCertificateFingerprint(nodelink.CertificateFingerprint(wrongCoordinatorLeaf), coordinatorLeaf)
	if !errors.Is(err, nodelink.ErrWrongCertificate) {
		t.Fatalf("VerifyCertificateFingerprint error = %v, want ErrWrongCertificate", err)
	}
}

func TestPinnedTLSRejectsExpiredCertificate(t *testing.T) {
	_, certificate := testCertificate(t, "expired")
	certificate.NotAfter = time.Now().Add(-time.Minute)
	err := nodelink.VerifyCertificateFingerprint(nodelink.CertificateFingerprint(certificate), certificate)
	if !errors.Is(err, nodelink.ErrWrongCertificate) {
		t.Fatalf("VerifyCertificateFingerprint error = %v, want ErrWrongCertificate", err)
	}
}

func TestSecureChannelRejectsOversizedEnvelope(t *testing.T) {
	coordinatorCertificate, coordinatorLeaf := testCertificate(t, "coordinator")
	executorCertificate, executorLeaf := testCertificate(t, "executor")
	serverRaw, clientRaw := net.Pipe()
	defer serverRaw.Close()
	defer clientRaw.Close()
	serverDone := make(chan *nodelink.SecureChannel, 1)
	go func() {
		channel, _ := nodelink.AcceptExecutor(context.Background(), serverRaw, nodelink.TLSIdentity{
			LocalComputerID: "coordinator-1", PeerComputerID: "executor-1", ExecutorComputerID: "executor-1",
			Certificate: coordinatorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(executorLeaf),
		}, 128)
		serverDone <- channel
	}()
	client, err := nodelink.DialCoordinator(context.Background(), pipeDialer{clientRaw}, "pipe", nodelink.TLSIdentity{
		LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1",
		Certificate: executorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(coordinatorLeaf),
	}, 128)
	if err != nil {
		t.Fatal(err)
	}
	server := <-serverDone
	if server == nil {
		t.Fatal("server TLS channel was not established")
	}
	envelope := commandEnvelope()
	envelope.Payload = []byte(`{"text":"` + strings.Repeat("x", 256) + `"}`)
	if err := client.WriteEnvelope(context.Background(), envelope); !errors.Is(err, nodelink.ErrFrameTooLarge) {
		t.Fatalf("WriteEnvelope error = %v, want ErrFrameTooLarge", err)
	}
}

func testCertificate(t *testing.T, commonName string) (tlsCertificate tls.Certificate, leaf *x509.Certificate) {
	t.Helper()
	return testCertificateWithValidity(t, commonName, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
}

func testCertificateWithValidity(t *testing.T, commonName string, notBefore, notAfter time.Time) (tlsCertificate tls.Certificate, leaf *x509.Certificate) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: commonName},
		NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: privateKey, Leaf: leaf}, leaf
}
