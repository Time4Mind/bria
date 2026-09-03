package nodebootstrap_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"bria/internal/nodebootstrap"
)

type shortWriteConn struct{ net.Conn }

func (connection shortWriteConn) Write(data []byte) (int, error) {
	if len(data) > 2 {
		data = data[:2]
	}
	return connection.Conn.Write(data)
}

type shortWriteDialer struct{ connection net.Conn }

func (dialer shortWriteDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return shortWriteConn{dialer.connection}, nil
}

func TestProtocolDemuxRoutesBootstrapAndPairedTLSClientsOnOneListener(t *testing.T) {
	listener := newInjectedListener()
	demux, err := nodebootstrap.NewProtocolDemux(listener, nodebootstrap.DemuxLimits{PrefaceTimeout: time.Second, MaxConcurrent: 2, QueueSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- demux.Run(ctx) }()
	for protocol, routed := range map[nodebootstrap.LinkProtocol]interface{ Accept() (net.Conn, error) }{
		nodebootstrap.ProtocolBootstrap: demux.BootstrapListener(),
		nodebootstrap.ProtocolPaired:    demux.PairedListener(),
	} {
		server, client := net.Pipe()
		listener.connections <- server
		dialer := nodebootstrap.ProtocolDialer{Dialer: pipeDialer{client}, Protocol: protocol}
		connection, err := dialer.DialContext(context.Background(), "tcp", "one-address")
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := routed.Accept()
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.Close()
		_ = accepted.Close()
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("demux Run error = %v", err)
	}
}

func TestProtocolDialerCompletesPrefaceAcrossShortWrites(t *testing.T) {
	listener := newInjectedListener()
	demux, err := nodebootstrap.NewProtocolDemux(listener, nodebootstrap.DemuxLimits{PrefaceTimeout: time.Second, MaxConcurrent: 1, QueueSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- demux.Run(ctx) }()
	server, client := net.Pipe()
	listener.connections <- server
	dialer := nodebootstrap.ProtocolDialer{Dialer: shortWriteDialer{client}, Protocol: nodebootstrap.ProtocolBootstrap}
	connection, err := dialer.DialContext(context.Background(), "tcp", "one-address")
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := demux.BootstrapListener().Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	_ = accepted.Close()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("demux Run error = %v", err)
	}
}
