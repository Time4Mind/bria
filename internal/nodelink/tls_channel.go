package nodelink

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"bria/internal/domain"
)

const DefaultMaxFrameBytes uint32 = 1 << 20
const DefaultTLSHandshakeTimeout = 10 * time.Second

var (
	ErrInvalidTLSIdentity = errors.New("invalid TLS channel identity")
	ErrWrongCertificate   = errors.New("peer certificate does not match pinned identity")
	ErrFrameTooLarge      = errors.New("node-link frame exceeds configured limit")
	ErrInvalidFrame       = errors.New("invalid node-link frame")
)

type RawDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type TLSIdentity struct {
	LocalComputerID       domain.ComputerID
	PeerComputerID        domain.ComputerID
	ExecutorComputerID    domain.ComputerID
	Certificate           tls.Certificate
	PeerCertificateSHA256 string
}

type SecureChannel struct {
	connection *tls.Conn
	identity   ChannelIdentity
	maxFrame   uint32
	writeMu    sync.Mutex
	readMu     sync.Mutex
}

func CertificateFingerprint(certificate *x509.Certificate) string {
	if certificate == nil {
		return ""
	}
	digest := sha256.Sum256(certificate.Raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func VerifyCertificateFingerprint(expected string, certificate *x509.Certificate) error {
	pin, err := parseFingerprint(expected)
	if err != nil {
		return err
	}
	if certificate == nil {
		return ErrWrongCertificate
	}
	if !certificateValidNow(certificate) {
		return ErrWrongCertificate
	}
	digest := sha256.Sum256(certificate.Raw)
	if subtle.ConstantTimeCompare(digest[:], pin) != 1 {
		return ErrWrongCertificate
	}
	return nil
}

func certificateValidNow(certificate *x509.Certificate) bool {
	if certificate == nil {
		return false
	}
	now := time.Now()
	return !now.Before(certificate.NotBefore) && !now.After(certificate.NotAfter)
}

// DialCoordinator is the only constructor for a new outbound node-link
// channel. It requires that the dialing endpoint is the executor.
func DialCoordinator(ctx context.Context, dialer RawDialer, address string, identity TLSIdentity, maxFrameBytes uint32) (*SecureChannel, error) {
	if dialer == nil || strings.TrimSpace(address) == "" || identity.ExecutorComputerID != identity.LocalComputerID {
		return nil, ErrInvalidTLSIdentity
	}
	tlsConfig, err := pinnedTLSConfig(identity, false)
	if err != nil {
		return nil, err
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, DefaultTLSHandshakeTimeout)
	defer cancel()
	raw, err := dialer.DialContext(handshakeCtx, "tcp", address)
	if err != nil {
		return nil, err
	}
	connection := tls.Client(raw, tlsConfig)
	if err := connection.HandshakeContext(handshakeCtx); err != nil {
		_ = connection.NetConn().Close()
		return nil, normalizeTLSError(err)
	}
	return newSecureChannel(connection, identity, maxFrameBytes), nil
}

// AcceptExecutor authenticates an already accepted inbound connection. It
// cannot dial an executor and requires the peer to be the executor identity.
func AcceptExecutor(ctx context.Context, raw net.Conn, identity TLSIdentity, maxFrameBytes uint32) (*SecureChannel, error) {
	if raw == nil || identity.ExecutorComputerID != identity.PeerComputerID {
		return nil, ErrInvalidTLSIdentity
	}
	tlsConfig, err := pinnedTLSConfig(identity, true)
	if err != nil {
		return nil, err
	}
	connection := tls.Server(raw, tlsConfig)
	handshakeCtx, cancel := context.WithTimeout(ctx, DefaultTLSHandshakeTimeout)
	defer cancel()
	if err := connection.HandshakeContext(handshakeCtx); err != nil {
		_ = connection.NetConn().Close()
		return nil, normalizeTLSError(err)
	}
	return newSecureChannel(connection, identity, maxFrameBytes), nil
}

func newSecureChannel(connection *tls.Conn, identity TLSIdentity, maxFrameBytes uint32) *SecureChannel {
	if maxFrameBytes == 0 {
		maxFrameBytes = DefaultMaxFrameBytes
	}
	return &SecureChannel{
		connection: connection,
		identity: ChannelIdentity{
			LocalComputerID: identity.LocalComputerID, PeerComputerID: identity.PeerComputerID,
			ExecutorComputerID: identity.ExecutorComputerID, PeerCertificateSHA256: identity.PeerCertificateSHA256,
			MutuallyAuthenticated: true,
		},
		maxFrame: maxFrameBytes,
	}
}

func (channel *SecureChannel) Identity() ChannelIdentity {
	if channel == nil {
		return ChannelIdentity{}
	}
	return channel.identity
}

// TLSVersion exposes the negotiated protocol version for readiness evidence.
func (channel *SecureChannel) TLSVersion() uint16 {
	if channel == nil || channel.connection == nil {
		return 0
	}
	return channel.connection.ConnectionState().Version
}

func (channel *SecureChannel) Close() error {
	if channel == nil || channel.connection == nil {
		return nil
	}
	return channel.connection.NetConn().Close()
}

func (channel *SecureChannel) WriteEnvelope(ctx context.Context, envelope Envelope) error {
	if channel == nil || channel.connection == nil {
		return ErrInvalidFrame
	}
	if envelope.Version != ProtocolVersion {
		return ErrIncompatibleVersion
	}
	if err := validateEnvelope(envelope); err != nil {
		return err
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return ErrInvalidFrame
	}
	if uint64(len(encoded)) > uint64(channel.maxFrame) {
		return ErrFrameTooLarge
	}
	return channel.WriteFrame(ctx, encoded)
}

func (channel *SecureChannel) WriteFrame(ctx context.Context, encoded []byte) error {
	if channel == nil || channel.connection == nil || len(encoded) == 0 {
		return ErrInvalidFrame
	}
	if uint64(len(encoded)) > uint64(channel.maxFrame) {
		return ErrFrameTooLarge
	}
	channel.writeMu.Lock()
	defer channel.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := channel.connection.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer channel.connection.SetWriteDeadline(noDeadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = channel.connection.Close() })
	defer stop()
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(encoded)))
	if err := writeFull(channel.connection, header[:]); err != nil {
		return err
	}
	return writeFull(channel.connection, encoded)
}

func (channel *SecureChannel) ReadEnvelope(ctx context.Context) (Envelope, error) {
	encoded, err := channel.ReadFrame(ctx)
	if err != nil {
		return Envelope{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Envelope{}, ErrInvalidFrame
	}
	if envelope.Version != ProtocolVersion {
		return Envelope{}, ErrIncompatibleVersion
	}
	if err := validateEnvelope(envelope); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func (channel *SecureChannel) ReadFrame(ctx context.Context) ([]byte, error) {
	if channel == nil || channel.connection == nil {
		return nil, ErrInvalidFrame
	}
	channel.readMu.Lock()
	defer channel.readMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := channel.connection.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		defer channel.connection.SetReadDeadline(noDeadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = channel.connection.Close() })
	defer stop()
	var header [4]byte
	if _, err := io.ReadFull(channel.connection, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 {
		return nil, ErrInvalidFrame
	}
	if size > channel.maxFrame {
		return nil, ErrFrameTooLarge
	}
	encoded := make([]byte, size)
	if _, err := io.ReadFull(channel.connection, encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

var noDeadline = (time.Time{})

func pinnedTLSConfig(identity TLSIdentity, server bool) (*tls.Config, error) {
	if strings.TrimSpace(string(identity.LocalComputerID)) == "" || strings.TrimSpace(string(identity.PeerComputerID)) == "" || strings.TrimSpace(string(identity.ExecutorComputerID)) == "" || identity.LocalComputerID == identity.PeerComputerID || len(identity.Certificate.Certificate) == 0 || identity.Certificate.PrivateKey == nil {
		return nil, ErrInvalidTLSIdentity
	}
	if _, err := parseFingerprint(identity.PeerCertificateSHA256); err != nil {
		return nil, err
	}
	config := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{identity.Certificate},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return ErrWrongCertificate
			}
			return VerifyCertificateFingerprint(identity.PeerCertificateSHA256, state.PeerCertificates[0])
		},
	}
	if server {
		config.ClientAuth = tls.RequireAnyClientCert
	} else {
		// Name and CA validation are intentionally replaced by the pairing-time
		// certificate pin. The pin is mandatory and verified above.
		config.InsecureSkipVerify = true
	}
	return config, nil
}

func parseFingerprint(value string) ([]byte, error) {
	encoded := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != sha256.Size {
		return nil, ErrInvalidTLSIdentity
	}
	return decoded, nil
}

func normalizeTLSError(err error) error {
	if errors.Is(err, ErrWrongCertificate) || strings.Contains(err.Error(), ErrWrongCertificate.Error()) {
		return fmt.Errorf("%w: TLS handshake rejected peer", ErrWrongCertificate)
	}
	return err
}

func writeFull(writer io.Writer, data []byte) error {
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
