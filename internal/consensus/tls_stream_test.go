package consensus_test

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/security"
	"github.com/hashicorp/raft"
)

func TestTLSStreamAuthenticatesExpectedStableNodeID(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	ca, caPEM, _, err := security.GenerateCA("cluster-a", now, 3650*24*time.Hour)
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		t.Fatalf("roots: %v", err)
	}
	serverCredentials, err := security.IssueNodeCertificate(ca, "cluster-a", "server", now, 24*time.Hour)
	if err != nil {
		t.Fatalf("issue server: %v", err)
	}
	clientCredentials, err := security.IssueNodeCertificate(ca, "cluster-a", "client", now, 24*time.Hour)
	if err != nil {
		t.Fatalf("issue client: %v", err)
	}
	serverCertificate, err := tls.X509KeyPair(serverCredentials.CertificatePEM, serverCredentials.PrivateKeyPEM)
	if err != nil {
		t.Fatalf("server key pair: %v", err)
	}
	clientCertificate, err := tls.X509KeyPair(clientCredentials.CertificatePEM, clientCredentials.PrivateKeyPEM)
	if err != nil {
		t.Fatalf("client key pair: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serverResolver := consensus.NewStaticPeerResolver()
	serverResolver.ApproveNodeID("client")
	serverLayer, err := consensus.NewTLSStreamLayer(consensus.TLSStreamConfig{
		Listener: listener, Certificate: serverCertificate, Roots: roots,
		ClusterID: "cluster-a", LocalNodeID: "server", Resolver: serverResolver, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("server layer: %v", err)
	}
	t.Cleanup(func() { _ = serverLayer.Close() })

	clientListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	clientResolver := consensus.NewStaticPeerResolver()
	clientResolver.Set(raft.ServerAddress(listener.Addr().String()), "server")
	clientResolver.ApproveNodeID("server")
	clientLayer, err := consensus.NewTLSStreamLayer(consensus.TLSStreamConfig{
		Listener: clientListener, Certificate: clientCertificate, Roots: roots,
		ClusterID: "cluster-a", LocalNodeID: "client", Resolver: clientResolver, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("client layer: %v", err)
	}
	t.Cleanup(func() { _ = clientLayer.Close() })

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := serverLayer.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()
	client, err := clientLayer.Dial(raft.ServerAddress(listener.Addr().String()), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	select {
	case server := <-accepted:
		defer server.Close()
	case err := <-acceptErr:
		t.Fatalf("accept: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("server accept timed out")
	}

	wrongResolver := consensus.NewStaticPeerResolver()
	wrongResolver.Set(raft.ServerAddress(listener.Addr().String()), "other")
	wrongResolver.ApproveNodeID("other")
	wrongListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("wrong listen: %v", err)
	}
	wrongLayer, err := consensus.NewTLSStreamLayer(consensus.TLSStreamConfig{
		Listener: wrongListener, Certificate: clientCertificate, Roots: roots,
		ClusterID: "cluster-a", LocalNodeID: "client", Resolver: wrongResolver, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("wrong layer: %v", err)
	}
	defer wrongLayer.Close()
	go func() {
		connection, err := serverLayer.Accept()
		if err == nil {
			_ = connection.Close()
		}
	}()
	if connection, err := wrongLayer.Dial(raft.ServerAddress(listener.Addr().String()), 2*time.Second); err == nil {
		_ = connection.Close()
		t.Fatal("dial accepted certificate for wrong node identity")
	}
}

func TestTLSStreamRejectsCAValidButUnapprovedInboundNode(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	ca, caPEM, _, err := security.GenerateCA("cluster-a", now, 3650*24*time.Hour)
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		t.Fatalf("roots: %v", err)
	}
	serverCertificate := issueTLSCertificate(t, ca, "server", now)
	clientCertificate := issueTLSCertificate(t, ca, "unapproved", now)

	serverListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serverResolver := consensus.NewStaticPeerResolver()
	serverResolver.ApproveNodeID("approved-peer")
	serverLayer, err := consensus.NewTLSStreamLayer(consensus.TLSStreamConfig{
		Listener: serverListener, Certificate: serverCertificate, Roots: roots,
		ClusterID: "cluster-a", LocalNodeID: "server", Resolver: serverResolver, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("server layer: %v", err)
	}
	t.Cleanup(func() { _ = serverLayer.Close() })

	clientListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	clientResolver := consensus.NewStaticPeerResolver()
	clientResolver.Set(raft.ServerAddress(serverListener.Addr().String()), "server")
	clientResolver.ApproveNodeID("server")
	clientLayer, err := consensus.NewTLSStreamLayer(consensus.TLSStreamConfig{
		Listener: clientListener, Certificate: clientCertificate, Roots: roots,
		ClusterID: "cluster-a", LocalNodeID: "unapproved", Resolver: clientResolver, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("client layer: %v", err)
	}
	t.Cleanup(func() { _ = clientLayer.Close() })

	acceptErr := make(chan error, 1)
	go func() {
		connection, acceptError := serverLayer.Accept()
		if acceptError == nil {
			_ = connection.Close()
		}
		acceptErr <- acceptError
	}()
	connection, dialErr := clientLayer.Dial(raft.ServerAddress(serverListener.Addr().String()), 2*time.Second)
	if dialErr == nil {
		_ = connection.Close()
	}
	select {
	case err := <-acceptErr:
		if err == nil {
			t.Fatal("server accepted unapproved CA-valid node")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not finish rejected handshake")
	}
}

func TestStaticPeerResolverSeparatesDialMappingFromInboundApproval(t *testing.T) {
	resolver := consensus.NewStaticPeerResolver()
	address := raft.ServerAddress("peer.internal:7946")
	resolver.Set(address, "peer")
	if _, ok := resolver.ExpectedNodeID(address); !ok {
		t.Fatal("dial mapping was not stored")
	}
	if resolver.IsApprovedNodeID("peer") {
		t.Fatal("dial mapping unexpectedly approved inbound identity")
	}
	resolver.ApproveNodeID("peer")
	if !resolver.IsApprovedNodeID("peer") {
		t.Fatal("approved identity not reported")
	}
	resolver.RevokeNodeID("peer")
	if resolver.IsApprovedNodeID("peer") {
		t.Fatal("revoked identity remains approved")
	}
}

func TestStaticPeerResolverRejectsReplacedNodeKey(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	ca, _, _, err := security.GenerateCA("cluster-a", now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	current := issueTLSCertificate(t, ca, "peer", now)
	replacement := issueTLSCertificate(t, ca, "peer", now)
	currentLeaf, err := x509.ParseCertificate(current.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	replacementLeaf, err := x509.ParseCertificate(replacement.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := security.NodeCertificateFingerprint(currentLeaf)
	if err != nil {
		t.Fatal(err)
	}
	resolver := consensus.NewStaticPeerResolver()
	resolver.SetNodeFingerprint("peer", fingerprint)
	if !resolver.AuthorizeNodeCertificate("peer", currentLeaf) {
		t.Fatal("active key rejected")
	}
	if resolver.AuthorizeNodeCertificate("peer", replacementLeaf) {
		t.Fatal("replacement key without CA rotation link accepted")
	}
	newPublicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rotatedPEM, err := security.IssueRotatedNodeCertificateForPublicKey(
		ca, "cluster-a", "peer", newPublicKey, fingerprint, now, 24*time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	rotatedLeaf, err := security.ParseCertificate(rotatedPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !resolver.AuthorizeNodeCertificate("peer", rotatedLeaf) {
		t.Fatal("CA-linked rotation certificate rejected")
	}
	rotatedFingerprint, err := security.NodeCertificateFingerprint(rotatedLeaf)
	if err != nil {
		t.Fatal(err)
	}
	resolver.SetNodeFingerprint("peer", rotatedFingerprint)
	if resolver.AuthorizeNodeCertificate("peer", currentLeaf) {
		t.Fatal("old key accepted after rotation commit")
	}
}

func TestTLSStreamLayerPreservesAdvertisedHostname(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	layer, err := consensus.NewTLSStreamLayer(consensus.TLSStreamConfig{
		Listener: listener, AdvertiseAddress: "node.internal:7946",
		Certificate: tls.Certificate{Certificate: [][]byte{{1}}}, Roots: x509.NewCertPool(),
		ClusterID:   "test-cluster",
		LocalNodeID: "server", Resolver: consensus.NewStaticPeerResolver(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := layer.Addr().String(); got != "node.internal:7946" {
		t.Fatalf("advertised address = %q, want hostname preserved", got)
	}
}

func issueTLSCertificate(
	t *testing.T,
	ca security.CertificateAuthority,
	nodeID string,
	now time.Time,
) tls.Certificate {
	t.Helper()
	credentials, err := security.IssueNodeCertificate(ca, "cluster-a", nodeID, now, 24*time.Hour)
	if err != nil {
		t.Fatalf("issue %s: %v", nodeID, err)
	}
	certificate, err := tls.X509KeyPair(credentials.CertificatePEM, credentials.PrivateKeyPEM)
	if err != nil {
		t.Fatalf("parse %s key pair: %v", nodeID, err)
	}
	return certificate
}
