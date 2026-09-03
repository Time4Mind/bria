package nodebootstrap

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"bria/internal/nodelink"
)

type LinkProtocol byte

const (
	ProtocolPaired    LinkProtocol = 1
	ProtocolBootstrap LinkProtocol = 2
)

var protocolMagic = [4]byte{'B', 'R', 'I', 'A'}

type ProtocolDialer struct {
	Dialer   nodelink.RawDialer
	Protocol LinkProtocol
}

func (dialer ProtocolDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if dialer.Dialer == nil || (dialer.Protocol != ProtocolPaired && dialer.Protocol != ProtocolBootstrap) {
		return nil, ErrInvalidBootstrap
	}
	connection, err := dialer.Dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	preface := append(protocolMagic[:], byte(dialer.Protocol))
	if err := writePreface(connection, preface); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return connection, nil
}

func writePreface(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrUnexpectedEOF
		}
		data = data[written:]
	}
	return nil
}

type DemuxLimits struct {
	PrefaceTimeout time.Duration
	MaxConcurrent  int
	QueueSize      int
}

type ProtocolDemux struct {
	base      nodelink.RawListener
	limits    DemuxLimits
	paired    *routeListener
	bootstrap *routeListener
}

type routeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func NewProtocolDemux(base nodelink.RawListener, limits DemuxLimits) (*ProtocolDemux, error) {
	if base == nil || limits.PrefaceTimeout <= 0 || limits.MaxConcurrent <= 0 || limits.QueueSize <= 0 {
		return nil, ErrInvalidBootstrap
	}
	return &ProtocolDemux{base: base, limits: limits,
		paired:    &routeListener{connections: make(chan net.Conn, limits.QueueSize), closed: make(chan struct{})},
		bootstrap: &routeListener{connections: make(chan net.Conn, limits.QueueSize), closed: make(chan struct{})}}, nil
}

func (demux *ProtocolDemux) PairedListener() nodelink.RawListener    { return demux.paired }
func (demux *ProtocolDemux) BootstrapListener() nodelink.RawListener { return demux.bootstrap }

func (demux *ProtocolDemux) Run(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { _ = demux.base.Close() })
	defer stop()
	slots := make(chan struct{}, demux.limits.MaxConcurrent)
	var group sync.WaitGroup
	defer group.Wait()
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		connection, err := demux.base.Accept()
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		group.Add(1)
		go func() {
			defer group.Done()
			defer func() { <-slots }()
			if err := connection.SetReadDeadline(time.Now().Add(demux.limits.PrefaceTimeout)); err != nil {
				_ = connection.Close()
				return
			}
			var preface [5]byte
			if _, err := io.ReadFull(connection, preface[:]); err != nil || string(preface[:4]) != string(protocolMagic[:]) {
				_ = connection.Close()
				return
			}
			_ = connection.SetReadDeadline(time.Time{})
			var route *routeListener
			switch LinkProtocol(preface[4]) {
			case ProtocolPaired:
				route = demux.paired
			case ProtocolBootstrap:
				route = demux.bootstrap
			default:
				_ = connection.Close()
				return
			}
			select {
			case route.connections <- connection:
			case <-route.closed:
				_ = connection.Close()
			case <-ctx.Done():
				_ = connection.Close()
			}
		}()
	}
}

func (listener *routeListener) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.connections:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *routeListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return nil
}
