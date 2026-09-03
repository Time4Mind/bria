package nodelink_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/nodelink"
)

type injectedListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
	accepts     atomic.Int32
}

func newInjectedListener() *injectedListener {
	return &injectedListener{connections: make(chan net.Conn, 4), closed: make(chan struct{})}
}

func (listener *injectedListener) Accept() (net.Conn, error) {
	listener.accepts.Add(1)
	select {
	case connection := <-listener.connections:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func TestCoordinatorServerBoundsStalledHandshakesAndConcurrentAccepts(t *testing.T) {
	coordinatorCertificate, _ := testCertificate(t, "coordinator")
	listener := newInjectedListener()
	firstServer, firstClient := net.Pipe()
	secondServer, secondClient := net.Pipe()
	defer firstClient.Close()
	defer secondClient.Close()
	listener.connections <- firstServer
	listener.connections <- secondServer
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- nodelink.ServeCoordinatorWithLimits(context.Background(), listener, nodelink.CoordinatorTLSIdentity{
			ComputerID: "coordinator-1", Certificate: coordinatorCertificate,
		}, func(string) (domain.ComputerID, bool) { return "", false }, nodelink.ServerLimits{
			MaxConcurrentConnections: 1, HandshakeTimeout: 40 * time.Millisecond, MaxFrameBytes: 1024,
		}, func(context.Context, *nodelink.SecureChannel) error { return nil })
	}()
	time.Sleep(10 * time.Millisecond)
	if got := listener.accepts.Load(); got != 1 {
		t.Fatalf("Accept calls while first handshake is stalled = %d, want 1", got)
	}
	time.Sleep(80 * time.Millisecond)
	if got := listener.accepts.Load(); got < 2 {
		t.Fatalf("server did not release handshake slot after timeout; Accept calls = %d", got)
	}
	_ = listener.Close()
	if err := <-serverDone; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ServeCoordinatorWithLimits error = %v, want closed listener", err)
	}
}

func TestCoordinatorRejectsExpiredExecutorDuringRealHandshake(t *testing.T) {
	coordinatorCertificate, coordinatorLeaf := testCertificate(t, "coordinator")
	executorCertificate, executorLeaf := testCertificateWithValidity(t, "expired-executor", time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	listener := newInjectedListener()
	serverRaw, clientRaw := net.Pipe()
	listener.connections <- serverRaw
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handled := make(chan struct{}, 1)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- nodelink.ServeCoordinatorWithLimits(ctx, listener, nodelink.CoordinatorTLSIdentity{
			ComputerID: "coordinator-1", Certificate: coordinatorCertificate,
		}, func(fingerprint string) (domain.ComputerID, bool) {
			return "executor-1", fingerprint == nodelink.CertificateFingerprint(executorLeaf)
		}, nodelink.ServerLimits{MaxConcurrentConnections: 1, HandshakeTimeout: time.Second, MaxFrameBytes: 1024}, func(context.Context, *nodelink.SecureChannel) error {
			handled <- struct{}{}
			return nil
		})
	}()
	clientCtx, clientCancel := context.WithTimeout(context.Background(), time.Second)
	defer clientCancel()
	client, clientErr := nodelink.DialCoordinator(clientCtx, pipeDialer{clientRaw}, "pipe", nodelink.TLSIdentity{
		LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1",
		Certificate: executorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(coordinatorLeaf),
	}, 1024)
	if clientErr == nil {
		defer client.Close()
		select {
		case <-handled:
			t.Fatal("expired executor reached authenticated handler")
		case <-time.After(100 * time.Millisecond):
		}
	}
	cancel()
	if err := <-serverDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeCoordinatorWithLimits error = %v", err)
	}
}

func (listener *injectedListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return nil
}

func TestCoordinatorServerAcceptsOnlyAuthorizedExecutorFromInjectedListener(t *testing.T) {
	coordinatorCertificate, coordinatorLeaf := testCertificate(t, "coordinator")
	executorCertificate, executorLeaf := testCertificate(t, "executor")
	listener := newInjectedListener()
	serverRaw, clientRaw := net.Pipe()
	listener.connections <- serverRaw
	ctx, cancel := context.WithCancel(context.Background())
	handled := make(chan nodelink.ChannelIdentity, 1)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- nodelink.ServeCoordinator(ctx, listener, nodelink.CoordinatorTLSIdentity{
			ComputerID: "coordinator-1", Certificate: coordinatorCertificate,
		}, func(fingerprint string) (domain.ComputerID, bool) {
			return "executor-1", fingerprint == nodelink.CertificateFingerprint(executorLeaf)
		}, 1024, func(_ context.Context, channel *nodelink.SecureChannel) error {
			handled <- channel.Identity()
			cancel()
			return nil
		})
	}()
	client, err := nodelink.DialCoordinator(context.Background(), pipeDialer{clientRaw}, "pipe", nodelink.TLSIdentity{
		LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1",
		Certificate: executorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(coordinatorLeaf),
	}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	identity := <-handled
	if !identity.MutuallyAuthenticated || identity.PeerComputerID != "executor-1" || identity.ExecutorComputerID != "executor-1" {
		t.Fatalf("accepted identity = %#v", identity)
	}
	if err := <-serverDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeCoordinator error = %v, want context canceled", err)
	}
}
