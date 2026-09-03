package multinodecomposition_test

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
	"os"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/multinodecomposition"
	"bria/internal/nodelink"
)

func TestFileEventOutboxPersistsOfflineEventAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	outbox, err := multinodecomposition.OpenFileEventOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	event := executorEvent("event-1")
	if err := outbox.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	reopened, err := multinodecomposition.OpenFileEventOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	pending := reopened.Pending()
	if len(pending) != 1 || pending[0].OperationID != event.OperationID || string(pending[0].Payload) != string(event.Payload) {
		t.Fatalf("reopened pending = %#v, want exact event %#v", pending, event)
	}
}

func TestFileEventOutboxRejectsLossySnapshotAndSymlink(t *testing.T) {
	dir := t.TempDir()
	lossy := filepath.Join(dir, "lossy.json")
	if err := os.WriteFile(lossy, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := multinodecomposition.OpenFileEventOutbox(lossy); !errors.Is(err, multinodecomposition.ErrInvalidComposition) {
		t.Fatalf("lossy snapshot error=%v", err)
	}
	valid := filepath.Join(dir, "valid.json")
	outbox, _ := multinodecomposition.OpenFileEventOutbox(valid)
	if err := outbox.Enqueue(context.Background(), executorEvent("event-1")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.json")
	if err := os.Symlink(valid, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := multinodecomposition.OpenFileEventOutbox(alias); !errors.Is(err, multinodecomposition.ErrInvalidComposition) {
		t.Fatalf("symlink outbox error=%v", err)
	}
}

func TestFileEventOutboxKeepsLostReceiptAndDeletesOnlyExactAcknowledgement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	outbox, _ := multinodecomposition.OpenFileEventOutbox(path)
	event := executorEvent("event-1")
	if err := outbox.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	executorChannel, coordinatorChannel := securePair(t)
	read := make(chan error, 1)
	go func() {
		defer coordinatorChannel.Close()
		_, err := coordinatorChannel.ReadEnvelope(context.Background())
		read <- err
	}()
	if err := outbox.Deliver(context.Background(), executorChannel); err == nil {
		t.Fatal("delivery without acknowledgement succeeded")
	}
	_ = executorChannel.Close()
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	reopened, err := multinodecomposition.OpenFileEventOutbox(path)
	if err != nil || len(reopened.Pending()) != 1 {
		t.Fatalf("lost receipt reopen pending=%#v err=%v", reopened.Pending(), err)
	}

	executorChannel, coordinatorChannel = securePair(t)
	serverDone := make(chan error, 1)
	go func() {
		defer coordinatorChannel.Close()
		got, err := coordinatorChannel.ReadEnvelope(context.Background())
		if err == nil {
			ack := nodelink.Envelope{
				Version: nodelink.ProtocolVersion, Kind: nodelink.KindAcknowledgement, OperationID: got.OperationID,
				Generation: got.Generation, CoordinatorID: got.CoordinatorID, SourceComputerID: got.CoordinatorID,
				TargetComputerID: got.SourceComputerID, Payload: []byte(`{"accepted":true}`),
			}
			err = coordinatorChannel.WriteEnvelope(context.Background(), ack)
		}
		serverDone <- err
	}()
	if err := reopened.Deliver(context.Background(), executorChannel); err != nil {
		t.Fatal(err)
	}
	_ = executorChannel.Close()
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	physical, err := multinodecomposition.OpenFileEventOutbox(path)
	if err != nil || len(physical.Pending()) != 0 {
		t.Fatalf("acknowledged event remained after physical reopen: %#v err=%v", physical.Pending(), err)
	}
}

func executorEvent(id string) nodelink.Envelope {
	return nodelink.Envelope{
		Version: nodelink.ProtocolVersion, Kind: nodelink.KindEvent, OperationID: id,
		Generation: 1, CoordinatorID: "coordinator-1", SourceComputerID: "executor-1",
		TargetComputerID: "coordinator-1", Payload: []byte(`{"kind":"final","text":"done"}`),
	}
}

type pipeDialer struct{ connection net.Conn }

func (dialer pipeDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return dialer.connection, nil
}

func securePair(t *testing.T) (*nodelink.SecureChannel, *nodelink.SecureChannel) {
	t.Helper()
	coordinatorCertificate, coordinatorLeaf := testCertificate(t, "coordinator")
	executorCertificate, executorLeaf := testCertificate(t, "executor")
	serverRaw, clientRaw := net.Pipe()
	result := make(chan struct {
		channel *nodelink.SecureChannel
		err     error
	}, 1)
	go func() {
		channel, err := nodelink.AcceptExecutor(context.Background(), serverRaw, nodelink.TLSIdentity{
			LocalComputerID: "coordinator-1", PeerComputerID: "executor-1", ExecutorComputerID: "executor-1",
			Certificate: coordinatorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(executorLeaf),
		}, nodelink.DefaultMaxFrameBytes)
		result <- struct {
			channel *nodelink.SecureChannel
			err     error
		}{channel, err}
	}()
	executorChannel, err := nodelink.DialCoordinator(context.Background(), pipeDialer{clientRaw}, "one-address", nodelink.TLSIdentity{
		LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1",
		Certificate: executorCertificate, PeerCertificateSHA256: nodelink.CertificateFingerprint(coordinatorLeaf),
	}, nodelink.DefaultMaxFrameBytes)
	if err != nil {
		t.Fatal(err)
	}
	server := <-result
	if server.err != nil {
		t.Fatal(server.err)
	}
	return executorChannel, server.channel
}

func testCertificate(t *testing.T, commonName string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: commonName},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
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
