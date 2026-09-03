package nodebootstrap_test

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
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bria/internal/nodebootstrap"
	"bria/internal/nodelink"
)

type pipeDialer struct{ connection net.Conn }

func (dialer pipeDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return dialer.connection, nil
}

type injectedListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func newInjectedListener() *injectedListener {
	return &injectedListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
}

func (listener *injectedListener) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.connections:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *injectedListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return nil
}

func TestBootstrapChannelCreatesBoundedChallengeButCannotAuthorizeCommands(t *testing.T) {
	pairings, err := nodelink.OpenPairingFile(filepath.Join(t.TempDir(), "pairing.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := nodebootstrap.NewBootstrapService(pairings, nodebootstrap.BootstrapLimits{
		ChallengeTTL: time.Minute, Window: time.Hour, MaxChallengesPerFingerprint: 1,
		MaxChallengesPerSource: 2, MaxChallengesGlobal: 3, MaxPendingChallenges: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinatorCertificate, coordinatorLeaf := testCertificate(t, "coordinator")
	executorCertificate, executorLeaf := testCertificate(t, "unpaired-executor")
	listener := newInjectedListener()
	serverRaw, clientRaw := net.Pipe()
	listener.connections <- serverRaw
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- nodebootstrap.ServeBootstrap(ctx, listener, nodelink.CoordinatorTLSIdentity{
			ComputerID: "coordinator-1", Certificate: coordinatorCertificate,
		}, service, nodelink.ServerLimits{MaxConcurrentConnections: 1, HandshakeTimeout: time.Second, MaxFrameBytes: 2048})
	}()
	request := nodebootstrap.BootstrapRequest{ChallengeID: "challenge-1", ComputerID: "executor-1", Name: "Laptop", Code: "123456"}
	receipt, err := nodebootstrap.RequestPairing(context.Background(), pipeDialer{clientRaw}, "pipe", nodelink.TLSIdentity{
		LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1",
		Certificate: executorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(coordinatorLeaf),
	}, request, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ChallengeID != request.ChallengeID || receipt.Fingerprint != nodelink.CertificateFingerprint(executorLeaf) {
		t.Fatalf("bootstrap receipt = %#v", receipt)
	}
	if pairings.Authorized("executor-1", receipt.Fingerprint) {
		t.Fatal("bootstrap challenge authorized executor before Telegram confirmation")
	}
	if _, err := service.Register(receipt.Fingerprint, nodebootstrap.BootstrapRequest{Version: nodelink.ProtocolVersion, ChallengeID: "challenge-2", ComputerID: "executor-1", Name: "Laptop", Code: "654321"}, time.Now()); !errors.Is(err, nodebootstrap.ErrBootstrapRateLimited) {
		t.Fatalf("second bootstrap error = %v, want ErrBootstrapRateLimited", err)
	}
	if _, err := pairings.Confirm(request.ChallengeID, request.Code, receipt.Fingerprint, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !pairings.Authorized("executor-1", receipt.Fingerprint) {
		t.Fatal("Telegram-confirmed pairing grant did not authorize executor")
	}
	cancel()
	if err := <-serverDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeBootstrap error = %v", err)
	}
}

func TestBootstrapLimitsCertificateRotationPrunesExpiredAndRejectsUnsafeIdentity(t *testing.T) {
	pairings, err := nodelink.OpenPairingFile(filepath.Join(t.TempDir(), "pairing.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := nodebootstrap.NewBootstrapService(pairings, nodebootstrap.BootstrapLimits{
		ChallengeTTL: time.Minute, Window: time.Minute, MaxChallengesPerFingerprint: 2,
		MaxChallengesPerSource: 2, MaxChallengesGlobal: 1, MaxPendingChallenges: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	request := nodebootstrap.BootstrapRequest{Version: nodelink.ProtocolVersion, ChallengeID: "challenge-1", ComputerID: "executor-1", Name: "Laptop", Code: "123456"}
	if _, err := service.RegisterFrom("192.0.2.1", "sha256:first", request, now); err != nil {
		t.Fatal(err)
	}
	rotated := request
	rotated.ChallengeID = "challenge-2"
	if _, err := service.RegisterFrom("192.0.2.2", "sha256:rotated", rotated, now); !errors.Is(err, nodebootstrap.ErrBootstrapRateLimited) {
		t.Fatalf("rotated fingerprint error = %v", err)
	}
	rotated.ChallengeID = "challenge-3"
	if _, err := service.RegisterFrom("192.0.2.2", "sha256:rotated", rotated, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("request after prune = %v", err)
	}
	if got := pairings.PendingCount(); got != 1 {
		t.Fatalf("pending challenges after prune = %d, want 1", got)
	}
	unsafe := request
	unsafe.ChallengeID = "challenge-4"
	unsafe.Name = "Laptop\n✅ Trusted"
	if _, err := service.RegisterFrom("192.0.2.3", "sha256:unsafe", unsafe, now.Add(4*time.Minute)); !errors.Is(err, nodebootstrap.ErrInvalidBootstrap) {
		t.Fatalf("unsafe display name error = %v", err)
	}
	unsafe.Name = "Laptop"
	unsafe.Version = 0
	if _, err := service.RegisterFrom("192.0.2.3", "sha256:unsafe", unsafe, now.Add(4*time.Minute)); !errors.Is(err, nodebootstrap.ErrInvalidBootstrap) {
		t.Fatalf("wire version zero error = %v", err)
	}
}

func testCertificate(t *testing.T, commonName string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: commonName}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: privateKey, Leaf: leaf}, leaf
}
