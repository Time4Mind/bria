package nodelink

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"bria/internal/domain"
)

type RawListener interface {
	Accept() (net.Conn, error)
	Close() error
}

type CoordinatorTLSIdentity struct {
	ComputerID  domain.ComputerID
	Certificate tls.Certificate
}

type CertificateAuthorizer func(fingerprint string) (domain.ComputerID, bool)

type ServerLimits struct {
	MaxConcurrentConnections int
	HandshakeTimeout         time.Duration
	MaxFrameBytes            uint32
}

func DefaultServerLimits() ServerLimits {
	return ServerLimits{MaxConcurrentConnections: 64, HandshakeTimeout: DefaultTLSHandshakeTimeout, MaxFrameBytes: DefaultMaxFrameBytes}
}

func AcceptPairedExecutor(ctx context.Context, raw net.Conn, coordinator CoordinatorTLSIdentity, authorize CertificateAuthorizer, maxFrameBytes uint32) (*SecureChannel, error) {
	return acceptPairedExecutor(ctx, raw, coordinator, authorize, maxFrameBytes, DefaultTLSHandshakeTimeout)
}

func acceptPairedExecutor(ctx context.Context, raw net.Conn, coordinator CoordinatorTLSIdentity, authorize CertificateAuthorizer, maxFrameBytes uint32, handshakeTimeout time.Duration) (*SecureChannel, error) {
	if raw == nil || strings.TrimSpace(string(coordinator.ComputerID)) == "" || len(coordinator.Certificate.Certificate) == 0 || coordinator.Certificate.PrivateKey == nil || authorize == nil {
		return nil, ErrInvalidTLSIdentity
	}
	var peerID domain.ComputerID
	var peerFingerprint string
	config := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{coordinator.Certificate},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return ErrWrongCertificate
			}
			certificate := state.PeerCertificates[0]
			if !certificateValidNow(certificate) {
				return ErrWrongCertificate
			}
			peerFingerprint = CertificateFingerprint(certificate)
			id, allowed := authorize(peerFingerprint)
			if !allowed || strings.TrimSpace(string(id)) == "" || id == coordinator.ComputerID {
				return ErrWrongCertificate
			}
			peerID = id
			return nil
		},
	}
	connection := tls.Server(raw, config)
	handshakeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := connection.HandshakeContext(handshakeCtx); err != nil {
		_ = connection.NetConn().Close()
		return nil, normalizeTLSError(err)
	}
	return newSecureChannel(connection, TLSIdentity{
		LocalComputerID: coordinator.ComputerID, PeerComputerID: peerID, ExecutorComputerID: peerID,
		PeerCertificateSHA256: peerFingerprint,
	}, maxFrameBytes), nil
}

func ServeCoordinator(ctx context.Context, listener RawListener, coordinator CoordinatorTLSIdentity, authorize CertificateAuthorizer, maxFrameBytes uint32, handle func(context.Context, *SecureChannel) error) error {
	limits := DefaultServerLimits()
	if maxFrameBytes != 0 {
		limits.MaxFrameBytes = maxFrameBytes
	}
	return ServeCoordinatorWithLimits(ctx, listener, coordinator, authorize, limits, handle)
}

func ServeCoordinatorWithLimits(ctx context.Context, listener RawListener, coordinator CoordinatorTLSIdentity, authorize CertificateAuthorizer, limits ServerLimits, handle func(context.Context, *SecureChannel) error) error {
	if listener == nil || handle == nil || authorize == nil {
		return ErrInvalidTLSIdentity
	}
	if limits.MaxConcurrentConnections <= 0 || limits.HandshakeTimeout <= 0 {
		return ErrInvalidTLSIdentity
	}
	if limits.MaxFrameBytes == 0 {
		limits.MaxFrameBytes = DefaultMaxFrameBytes
	}
	stopClose := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stopClose()
	var handlers sync.WaitGroup
	defer handlers.Wait()
	slots := make(chan struct{}, limits.MaxConcurrentConnections)
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		raw, err := listener.Accept()
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			return err
		}
		handlers.Add(1)
		go func(connection net.Conn) {
			defer handlers.Done()
			defer func() { <-slots }()
			channel, err := acceptPairedExecutor(ctx, connection, coordinator, authorize, limits.MaxFrameBytes, limits.HandshakeTimeout)
			if err != nil {
				_ = connection.Close()
				return
			}
			defer channel.Close()
			_ = handle(ctx, channel)
		}(raw)
	}
}
