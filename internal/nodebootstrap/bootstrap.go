package nodebootstrap

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"bria/internal/nodelink"
)

var (
	ErrInvalidBootstrap     = errors.New("invalid pairing bootstrap request")
	ErrBootstrapRateLimited = errors.New("pairing bootstrap rate limit exceeded")
)

type BootstrapRequest struct {
	Version     uint16 `json:"version"`
	ChallengeID string `json:"challenge_id"`
	ComputerID  string `json:"computer_id"`
	Name        string `json:"name"`
	Code        string `json:"code"`
}

type BootstrapReceipt struct {
	Version     uint16    `json:"version"`
	ChallengeID string    `json:"challenge_id"`
	Fingerprint string    `json:"fingerprint"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type BootstrapLimits struct {
	ChallengeTTL                time.Duration
	Window                      time.Duration
	MaxChallengesPerFingerprint int
	MaxChallengesPerSource      int
	MaxChallengesGlobal         int
	MaxPendingChallenges        int
}

type bootstrapWindow struct {
	start time.Time
	count int
}

type BootstrapService struct {
	mu       sync.Mutex
	pairings *nodelink.PairingFile
	limits   BootstrapLimits
	attempts map[string]bootstrapWindow
	sources  map[string]bootstrapWindow
	global   bootstrapWindow
}

func NewBootstrapService(pairings *nodelink.PairingFile, limits BootstrapLimits) (*BootstrapService, error) {
	if pairings == nil || limits.ChallengeTTL <= 0 || limits.ChallengeTTL > nodelink.PairingReplayRetention || limits.Window <= 0 || limits.MaxChallengesPerFingerprint <= 0 || limits.MaxChallengesPerSource <= 0 || limits.MaxChallengesGlobal <= 0 || limits.MaxPendingChallenges <= 0 {
		return nil, ErrInvalidBootstrap
	}
	return &BootstrapService{pairings: pairings, limits: limits, attempts: make(map[string]bootstrapWindow), sources: make(map[string]bootstrapWindow)}, nil
}

func (service *BootstrapService) Register(fingerprint string, request BootstrapRequest, now time.Time) (BootstrapReceipt, error) {
	return service.RegisterFrom("local", fingerprint, request, now)
}

func (service *BootstrapService) RegisterFrom(source, fingerprint string, request BootstrapRequest, now time.Time) (BootstrapReceipt, error) {
	if service == nil || !validBootstrapRequest(request) || strings.TrimSpace(source) == "" || strings.TrimSpace(fingerprint) == "" || now.IsZero() {
		return BootstrapReceipt{}, ErrInvalidBootstrap
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if !now.Before(service.global.start.Add(service.limits.Window)) {
		service.global = bootstrapWindow{start: now}
		service.attempts = make(map[string]bootstrapWindow)
		service.sources = make(map[string]bootstrapWindow)
	}
	if service.global.count >= service.limits.MaxChallengesGlobal {
		return BootstrapReceipt{}, ErrBootstrapRateLimited
	}
	window := service.attempts[fingerprint]
	if window.start.IsZero() || !now.Before(window.start.Add(service.limits.Window)) {
		window = bootstrapWindow{start: now}
	}
	if window.count >= service.limits.MaxChallengesPerFingerprint {
		return BootstrapReceipt{}, ErrBootstrapRateLimited
	}
	sourceWindow := service.sources[source]
	if sourceWindow.start.IsZero() || !now.Before(sourceWindow.start.Add(service.limits.Window)) {
		sourceWindow = bootstrapWindow{start: now}
	}
	if sourceWindow.count >= service.limits.MaxChallengesPerSource {
		return BootstrapReceipt{}, ErrBootstrapRateLimited
	}
	if err := service.pairings.PruneExpired(now); err != nil {
		return BootstrapReceipt{}, err
	}
	if service.pairings.PendingCount() >= service.limits.MaxPendingChallenges {
		return BootstrapReceipt{}, ErrBootstrapRateLimited
	}
	window.count++
	service.attempts[fingerprint] = window
	sourceWindow.count++
	service.sources[source] = sourceWindow
	service.global.count++
	challenge, err := nodelink.NewPairingChallenge(request.ChallengeID, request.ComputerID, request.Name, request.Code, fingerprint, now.Add(service.limits.ChallengeTTL))
	if err != nil {
		return BootstrapReceipt{}, err
	}
	if err := service.pairings.Issue(challenge, now); err != nil {
		return BootstrapReceipt{}, err
	}
	return BootstrapReceipt{Version: nodelink.ProtocolVersion, ChallengeID: challenge.ID, Fingerprint: fingerprint, ExpiresAt: challenge.ExpiresAt}, nil
}

func validBootstrapRequest(request BootstrapRequest) bool {
	if request.Version != nodelink.ProtocolVersion {
		return false
	}
	if !safeIdentifier(request.ChallengeID, 128) || !safeIdentifier(request.ComputerID, 128) || !safeDisplayName(request.Name) || len(request.Code) != 6 {
		return false
	}
	for _, character := range request.Code {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func safeIdentifier(value string, maximum int) bool {
	if len(value) < 1 || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

func safeDisplayName(value string) bool {
	if !utf8.ValidString(value) || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 64 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			return false
		}
	}
	return true
}

func RequestPairing(ctx context.Context, dialer nodelink.RawDialer, address string, identity nodelink.TLSIdentity, request BootstrapRequest, maxFrame uint32) (BootstrapReceipt, error) {
	request.Version = nodelink.ProtocolVersion
	if !validBootstrapRequest(request) {
		return BootstrapReceipt{}, ErrInvalidBootstrap
	}
	channel, err := nodelink.DialCoordinator(ctx, dialer, address, identity, maxFrame)
	if err != nil {
		return BootstrapReceipt{}, err
	}
	defer channel.Close()
	encoded, err := json.Marshal(request)
	if err != nil {
		return BootstrapReceipt{}, ErrInvalidBootstrap
	}
	if err := channel.WriteFrame(ctx, encoded); err != nil {
		return BootstrapReceipt{}, err
	}
	var receipt BootstrapReceipt
	encoded, err = channel.ReadFrame(ctx)
	if err != nil {
		return BootstrapReceipt{}, err
	}
	if err := decodeBootstrapJSON(encoded, &receipt); err != nil {
		return BootstrapReceipt{}, err
	}
	if receipt.Version != nodelink.ProtocolVersion || receipt.ChallengeID != request.ChallengeID || receipt.Fingerprint == "" || receipt.ExpiresAt.IsZero() {
		return BootstrapReceipt{}, ErrInvalidBootstrap
	}
	leaf := identity.Certificate.Leaf
	if leaf == nil && len(identity.Certificate.Certificate) > 0 {
		leaf, err = x509.ParseCertificate(identity.Certificate.Certificate[0])
	}
	if err != nil || receipt.Fingerprint != nodelink.CertificateFingerprint(leaf) {
		return BootstrapReceipt{}, nodelink.ErrWrongCertificate
	}
	return receipt, nil
}

func ServeBootstrap(ctx context.Context, listener nodelink.RawListener, coordinator nodelink.CoordinatorTLSIdentity, service *BootstrapService, limits nodelink.ServerLimits) error {
	if listener == nil || service == nil || limits.MaxConcurrentConnections <= 0 || limits.HandshakeTimeout <= 0 {
		return ErrInvalidBootstrap
	}
	if limits.MaxFrameBytes == 0 {
		limits.MaxFrameBytes = nodelink.DefaultMaxFrameBytes
	}
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	slots := make(chan struct{}, limits.MaxConcurrentConnections)
	var group sync.WaitGroup
	defer group.Wait()
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
			return err
		}
		group.Add(1)
		go func() {
			defer group.Done()
			defer func() { <-slots }()
			defer raw.Close()
			_ = serveBootstrapConnection(ctx, raw, coordinator, service, limits)
		}()
	}
}

func serveBootstrapConnection(ctx context.Context, raw net.Conn, coordinator nodelink.CoordinatorTLSIdentity, service *BootstrapService, limits nodelink.ServerLimits) error {
	var fingerprint string
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{coordinator.Certificate}, ClientAuth: tls.RequireAnyClientCert, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 || nodelink.VerifyCertificateFingerprint(nodelink.CertificateFingerprint(state.PeerCertificates[0]), state.PeerCertificates[0]) != nil {
			return nodelink.ErrWrongCertificate
		}
		fingerprint = nodelink.CertificateFingerprint(state.PeerCertificates[0])
		return nil
	}}
	connection := tls.Server(raw, config)
	handshakeCtx, cancel := context.WithTimeout(ctx, limits.HandshakeTimeout)
	defer cancel()
	if err := connection.SetDeadline(time.Now().Add(limits.HandshakeTimeout)); err != nil {
		return err
	}
	defer connection.SetDeadline(time.Time{})
	if err := connection.HandshakeContext(handshakeCtx); err != nil {
		return err
	}
	var request BootstrapRequest
	if err := readBootstrapJSON(connection, &request, limits.MaxFrameBytes); err != nil {
		return err
	}
	source := raw.RemoteAddr().String()
	if host, _, splitErr := net.SplitHostPort(source); splitErr == nil {
		source = host
	}
	receipt, err := service.RegisterFrom(source, fingerprint, request, time.Now())
	if err != nil {
		return err
	}
	return writeBootstrapJSON(connection, receipt, limits.MaxFrameBytes)
}

func writeBootstrapJSON(writer io.Writer, value any, max uint32) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) == 0 {
		return ErrInvalidBootstrap
	}
	if uint64(len(data)) > uint64(max) {
		return nodelink.ErrFrameTooLarge
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if err := writeFull(writer, header[:]); err != nil {
		return err
	}
	return writeFull(writer, data)
}

func decodeBootstrapJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrInvalidBootstrap
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidBootstrap
	}
	return nil
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

func readBootstrapJSON(reader io.Reader, value any, max uint32) error {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > max {
		return nodelink.ErrFrameTooLarge
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(reader, data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrInvalidBootstrap
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidBootstrap
	}
	return nil
}
