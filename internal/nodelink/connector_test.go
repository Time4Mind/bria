package nodelink_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"bria/internal/nodelink"
)

type sequenceDialer struct {
	mu         sync.Mutex
	attempts   int
	connection net.Conn
	addresses  []string
}

func (dialer *sequenceDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	dialer.attempts++
	dialer.addresses = append(dialer.addresses, address)
	if dialer.attempts == 1 {
		return nil, errors.New("coordinator temporarily offline")
	}
	if dialer.connection == nil {
		return nil, errors.New("no more connections")
	}
	connection := dialer.connection
	dialer.connection = nil
	return connection, nil
}

func TestExecutorConnectorReconnectsOnlyToPinnedCoordinator(t *testing.T) {
	coordinatorCertificate, coordinatorLeaf := testCertificate(t, "coordinator")
	executorCertificate, executorLeaf := testCertificate(t, "executor")
	serverRaw, clientRaw := net.Pipe()
	defer serverRaw.Close()
	defer clientRaw.Close()
	dialer := &sequenceDialer{connection: clientRaw}
	identity := nodelink.TLSIdentity{
		LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1",
		Certificate: executorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(coordinatorLeaf),
	}
	connector, err := nodelink.NewExecutorConnector(dialer, "fixed-coordinator:443", identity, 1024, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		channel, err := nodelink.AcceptExecutor(context.Background(), serverRaw, nodelink.TLSIdentity{
			LocalComputerID: "coordinator-1", PeerComputerID: "executor-1", ExecutorComputerID: "executor-1",
			Certificate: coordinatorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(executorLeaf),
		}, 1024)
		if channel != nil {
			defer channel.Close()
		}
		serverDone <- err
	}()
	ctx, cancel := context.WithCancel(context.Background())
	handled := 0
	err = connector.Run(ctx, func(_ context.Context, channel *nodelink.SecureChannel) error {
		handled++
		if identity := channel.Identity(); identity.PeerComputerID != "coordinator-1" || identity.ExecutorComputerID != "executor-1" {
			t.Fatalf("reconnected identity = %#v", identity)
		}
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context canceled", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if handled != 1 || dialer.attempts != 2 {
		t.Fatalf("handled = %d, dial attempts = %d", handled, dialer.attempts)
	}
	for _, address := range dialer.addresses {
		if address != "fixed-coordinator:443" {
			t.Fatalf("connector changed coordinator address to %q", address)
		}
	}
}
