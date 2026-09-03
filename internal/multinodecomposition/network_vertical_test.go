package multinodecomposition_test

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bria/internal/computer"
	"bria/internal/multinodecomposition"
	"bria/internal/nodebootstrap"
	"bria/internal/nodelink"
)

type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func TestCoordinatorLedgerKeepsUnknownEffectAcrossPhysicalReopen(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "ledger-state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "operations.json")
	ledger, err := nodelink.OpenFileOperationLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	operation := nodelink.Operation{ID: "event-unknown", Digest: "digest-unknown"}
	moved := stateDir + "-moved"
	_, err = ledger.ApplyOnce(context.Background(), operation, func() error {
		if err := os.Rename(stateDir, moved); err != nil {
			return err
		}
		return os.WriteFile(stateDir, []byte("storage unavailable"), 0o600)
	})
	if removeErr := os.Remove(stateDir); removeErr != nil {
		t.Fatal(removeErr)
	}
	if renameErr := os.Rename(moved, stateDir); renameErr != nil {
		t.Fatal(renameErr)
	}
	if !errors.Is(err, nodelink.ErrOperationInDoubt) {
		t.Fatalf("interrupted final ledger commit error=%v", err)
	}
	if _, err := ledger.ApplyOnce(context.Background(), operation, func() error { return errors.New("must not auto-repeat") }); !errors.Is(err, nodelink.ErrOperationInDoubt) {
		t.Fatalf("live unknown ledger error=%v", err)
	}
	reopened, err := nodelink.OpenFileOperationLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.InDoubtOperations(); len(got) != 1 || got[0] != operation {
		t.Fatalf("physical unknown ledger=%#v", got)
	}
	if _, err := reopened.ApplyOnce(context.Background(), operation, func() error { return errors.New("must not auto-repeat") }); !errors.Is(err, nodelink.ErrOperationInDoubt) {
		t.Fatalf("unknown effect auto-repeat error=%v", err)
	}
	if err := reopened.Resolve(context.Background(), operation, nodelink.OperationApplied); err != nil {
		t.Fatal(err)
	}
	physical, err := nodelink.OpenFileOperationLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := physical.ApplyOnce(context.Background(), operation, func() error { return errors.New("resolved effect repeated") })
	if err != nil || !duplicate {
		t.Fatalf("resolved physical ledger duplicate=%v err=%v", duplicate, err)
	}
}

func newPipeListener() *pipeListener {
	return &pipeListener{connections: make(chan net.Conn, 8), closed: make(chan struct{})}
}

func (listener *pipeListener) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.connections:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *pipeListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return nil
}

type oneAddressDialer struct {
	mu        sync.Mutex
	listener  *pipeListener
	failures  int
	attempts  int
	addresses []string
}

func (dialer *oneAddressDialer) DialContext(ctx context.Context, _, address string) (net.Conn, error) {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	dialer.attempts++
	dialer.addresses = append(dialer.addresses, address)
	if dialer.failures > 0 {
		dialer.failures--
		return nil, errors.New("coordinator offline")
	}
	server, client := net.Pipe()
	select {
	case dialer.listener.connections <- server:
		return client, nil
	case <-ctx.Done():
		_ = server.Close()
		_ = client.Close()
		return nil, ctx.Err()
	}
}

func (dialer *oneAddressDialer) failNext(count int) {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	dialer.failures = count
}

func runOneAddressRolesReconnectAndReplayOfflineEventExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	coordinatorCertificate, coordinatorLeaf := testCertificate(t, "coordinator")
	executorCertificate, executorLeaf := testCertificate(t, "executor")
	listener := newPipeListener()
	dialer := &oneAddressDialer{listener: listener}
	pairings, err := nodelink.OpenPairingFile(filepath.Join(dir, "pairings.json"))
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := nodebootstrap.NewBootstrapService(pairings, nodebootstrap.BootstrapLimits{
		ChallengeTTL: time.Minute, Window: time.Hour, MaxChallengesPerFingerprint: 2,
		MaxChallengesPerSource: 2, MaxChallengesGlobal: 4, MaxPendingChallenges: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	fence, _ := computer.RestoreFence(computer.FenceSnapshot{CoordinatorID: "coordinator-1", Generation: 1})
	ledgerPath := filepath.Join(dir, "operations.json")
	ledger, _ := nodelink.OpenFileOperationLedger(ledgerPath)
	processor, _ := nodelink.NewProcessor("coordinator-1", fence, ledger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	applied, received := 0, 0
	var appliedMu sync.Mutex
	coordinator, err := multinodecomposition.NewCoordinatorRole(multinodecomposition.CoordinatorOptions{
		Listener: listener, Identity: nodelink.CoordinatorTLSIdentity{ComputerID: "coordinator-1", Certificate: coordinatorCertificate},
		Pairings: pairings, Bootstrap: bootstrap,
		Demux:   nodebootstrap.DemuxLimits{PrefaceTimeout: time.Second, MaxConcurrent: 4, QueueSize: 4},
		Network: nodelink.ServerLimits{MaxConcurrentConnections: 4, HandshakeTimeout: time.Second, MaxFrameBytes: 4096},
		Handle: func(callCtx context.Context, channel *nodelink.SecureChannel) error {
			identity := channel.Identity()
			if channel.TLSVersion() != tls.VersionTLS13 || !identity.MutuallyAuthenticated || identity.LocalComputerID != "coordinator-1" || identity.PeerComputerID != "executor-1" || identity.ExecutorComputerID != "executor-1" || identity.PeerCertificateSHA256 != nodelink.CertificateFingerprint(executorLeaf) {
				cancel()
				return errors.New("channel did not negotiate pinned TLS 1.3 mutual identity")
			}
			event, err := channel.ReadEnvelope(callCtx)
			if err != nil {
				return err
			}
			appliedMu.Lock()
			received++
			attempt := received
			appliedMu.Unlock()
			result, err := processor.Process(callCtx, channel.Identity(), event, func(context.Context, nodelink.Envelope) error {
				appliedMu.Lock()
				applied++
				appliedMu.Unlock()
				return nil
			})
			if err != nil {
				return err
			}
			if attempt == 1 {
				return nil // effect is durable but its receipt is deliberately lost
			}
			if !result.Duplicate {
				return errors.New("replay was not recognized as duplicate")
			}
			ack := nodelink.Envelope{
				Version: nodelink.ProtocolVersion, Kind: nodelink.KindAcknowledgement, OperationID: event.OperationID,
				Generation: event.Generation, CoordinatorID: event.CoordinatorID, SourceComputerID: event.CoordinatorID,
				TargetComputerID: event.SourceComputerID, Payload: []byte(`{"accepted":true}`),
			}
			if err := channel.WriteEnvelope(callCtx, ack); err != nil {
				return err
			}
			cancel()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinatorDone := make(chan error, 1)
	go func() { coordinatorDone <- coordinator.Run(ctx) }()

	request := nodebootstrap.BootstrapRequest{ChallengeID: "pair-1", ComputerID: "executor-1", Name: "Laptop", Code: "123456"}
	receipt, err := nodebootstrap.RequestPairing(context.Background(), nodebootstrap.ProtocolDialer{Dialer: dialer, Protocol: nodebootstrap.ProtocolBootstrap}, "one-address", nodelink.TLSIdentity{
		LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1",
		Certificate: executorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(coordinatorLeaf),
	}, request, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pairings.Confirm(request.ChallengeID, request.Code, receipt.Fingerprint, time.Now()); err != nil {
		t.Fatal(err)
	}
	dialer.failNext(1)
	executorOptions := multinodecomposition.ExecutorOptions{
		Dialer: dialer, CoordinatorAddress: "one-address",
		Identity: nodelink.TLSIdentity{
			LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1",
			Certificate: executorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(coordinatorLeaf),
		},
		OutboxPath: filepath.Join(dir, "events.json"), MaxFrameBytes: 4096, RetryDelay: time.Millisecond,
	}
	executor, err := multinodecomposition.NewExecutorRole(executorOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.EnqueueEvent(context.Background(), executorEvent("event-1")); err != nil {
		t.Fatal(err)
	}
	reopenedExecutor, err := multinodecomposition.NewExecutorRole(executorOptions)
	if err != nil || len(reopenedExecutor.PendingEvents()) != 1 {
		t.Fatalf("offline role reopen pending=%#v err=%v", reopenedExecutor.PendingEvents(), err)
	}
	if err := reopenedExecutor.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("executor Run error=%v", err)
	}
	if err := <-coordinatorDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("coordinator Run error=%v", err)
	}
	appliedMu.Lock()
	if applied != 1 || received != 2 {
		t.Fatalf("physical effect count=%d network deliveries=%d", applied, received)
	}
	appliedMu.Unlock()
	if len(reopenedExecutor.PendingEvents()) != 0 {
		t.Fatalf("acknowledged event remained queued: %#v", reopenedExecutor.PendingEvents())
	}
	physicalOutbox, err := multinodecomposition.OpenFileEventOutbox(filepath.Join(dir, "events.json"))
	if err != nil || len(physicalOutbox.Pending()) != 0 {
		t.Fatalf("physical outbox reopen=%#v err=%v", physicalOutbox.Pending(), err)
	}
	physicalLedger, err := nodelink.OpenFileOperationLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	reopenedProcessor, _ := nodelink.NewProcessor("coordinator-1", fence, physicalLedger)
	result, err := reopenedProcessor.Process(context.Background(), nodelink.ChannelIdentity{
		LocalComputerID: "coordinator-1", PeerComputerID: "executor-1", ExecutorComputerID: "executor-1", MutuallyAuthenticated: true,
	}, executorEvent("event-1"), func(context.Context, nodelink.Envelope) error { return errors.New("duplicate effect") })
	if err != nil || !result.Duplicate {
		t.Fatalf("physical ledger replay=%#v err=%v", result, err)
	}
	for _, address := range dialer.addresses {
		if address != "one-address" {
			t.Fatalf("role changed coordinator address to %q", address)
		}
	}
	if nodelink.CertificateFingerprint(executorLeaf) != receipt.Fingerprint {
		t.Fatal("bootstrap receipt did not pin the paired executor certificate")
	}
}

func TestExecutorRoleRejectsEventOutsideItsPinnedRole(t *testing.T) {
	coordinatorCertificate, coordinatorLeaf := testCertificate(t, "coordinator")
	executorCertificate, _ := testCertificate(t, "executor")
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	role, err := multinodecomposition.NewExecutorRole(multinodecomposition.ExecutorOptions{
		Dialer: pipeDialer{client}, CoordinatorAddress: "one-address",
		Identity: nodelink.TLSIdentity{
			LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1",
			Certificate: executorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(coordinatorLeaf),
		},
		OutboxPath: filepath.Join(t.TempDir(), "events.json"), RetryDelay: time.Millisecond,
	})
	_ = coordinatorCertificate
	if err != nil {
		t.Fatal(err)
	}
	foreign := executorEvent("event-foreign")
	foreign.CoordinatorID, foreign.TargetComputerID = "coordinator-2", "coordinator-2"
	if err := role.EnqueueEvent(context.Background(), foreign); !errors.Is(err, multinodecomposition.ErrInvalidComposition) {
		t.Fatalf("foreign role event error=%v", err)
	}
	if len(role.PendingEvents()) != 0 {
		t.Fatal("foreign role event was persisted")
	}
}
