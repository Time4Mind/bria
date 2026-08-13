package consensus_test

import (
	"net"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/security"
	"github.com/hashicorp/raft"
)

func TestTLSStreamDialsOverrideAndVerifiesAdvertisedIdentity(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	ca, caPEM, _, err := security.GenerateCA("cluster-a", now, 3650*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	serverCertificate := issueTLSCertificate(t, ca, "server", now)
	clientCertificate := issueTLSCertificate(t, ca, "client", now)
	serverListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverResolver := consensus.NewStaticPeerResolver()
	serverResolver.ApproveNodeID("client")
	serverLayer, err := consensus.NewTLSStreamLayer(consensus.TLSStreamConfig{
		Listener: serverListener, Certificate: serverCertificate, Roots: roots,
		ClusterID: "cluster-a", LocalNodeID: "server", Resolver: serverResolver,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverLayer.Close() })

	clientListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	advertised := raft.ServerAddress("server.bria.internal:7946")
	clientResolver := consensus.NewStaticPeerResolver()
	clientResolver.Set(advertised, "server")
	clientResolver.SetDialAddress("server", serverListener.Addr().String())
	clientResolver.ApproveNodeID("server")
	clientLayer, err := consensus.NewTLSStreamLayer(consensus.TLSStreamConfig{
		Listener: clientListener, Certificate: clientCertificate, Roots: roots,
		ClusterID: "cluster-a", LocalNodeID: "client", Resolver: clientResolver,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientLayer.Close() })

	accepted := make(chan error, 1)
	go func() {
		connection, acceptErr := serverLayer.Accept()
		if connection != nil {
			_ = connection.Close()
		}
		accepted <- acceptErr
	}()
	connection, err := clientLayer.Dial(advertised, 2*time.Second)
	if err != nil {
		t.Fatalf("dial through override: %v", err)
	}
	_ = connection.Close()
	if err := <-accepted; err != nil {
		t.Fatalf("accept override connection: %v", err)
	}

	wrongListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wrongResolver := consensus.NewStaticPeerResolver()
	wrongResolver.Set(advertised, "different-server")
	wrongResolver.SetDialAddress("different-server", serverListener.Addr().String())
	wrongResolver.ApproveNodeID("different-server")
	wrongLayer, err := consensus.NewTLSStreamLayer(consensus.TLSStreamConfig{
		Listener: wrongListener, Certificate: clientCertificate, Roots: roots,
		ClusterID: "cluster-a", LocalNodeID: "client", Resolver: wrongResolver,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrongLayer.Close() })
	go func() {
		connection, _ := serverLayer.Accept()
		if connection != nil {
			_ = connection.Close()
		}
	}()
	if connection, dialErr := wrongLayer.Dial(advertised, 2*time.Second); dialErr == nil {
		_ = connection.Close()
		t.Fatal("dial override bypassed advertised node identity verification")
	}
}
