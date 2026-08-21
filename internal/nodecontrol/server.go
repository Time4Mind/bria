package nodecontrol

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/Time4Mind/bria/internal/backendsetup"
	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/providerauth"
	"github.com/Time4Mind/bria/internal/providerstop"
	"github.com/Time4Mind/bria/internal/sessiondescription"
	"github.com/Time4Mind/bria/internal/sessionstart"
	"github.com/Time4Mind/bria/internal/speechsetup"
)

type ServerConfig struct {
	NodeID        string
	ClusterID     string
	Certificate   tls.Certificate
	Roots         *x509.CertPool
	Leadership    Leadership
	Health        HealthObserver
	Backups       ClusterSnapshotter
	Admin         MembershipAdmin
	Membership    Membership
	Service       RuntimeClient
	Heartbeats    HeartbeatCommitter
	Recovery      RecoveryCommitter
	Transcripts   TranscriptReader
	Descriptions  sessiondescription.Service
	SessionFiles  SessionFileReader
	Starts        sessionstart.Service
	ProviderAuth  providerauth.Service
	ProviderStops providerstop.Service
	BackendSetup  backendsetup.Service
	SpeechSetup   speechsetup.Service
	Updates       clusterupdate.Service
	Enrollments   EnrollmentCommitter
	// EnrollmentIssuerID is the only member allowed to forward a contract
	// validated by the public enrollment endpoint.
	EnrollmentIssuerID string
}

type Server struct {
	nodeID             string
	clusterID          string
	leadership         Leadership
	health             HealthObserver
	backups            ClusterSnapshotter
	admin              MembershipAdmin
	backupCertificate  tls.Certificate
	membership         Membership
	service            RuntimeClient
	heartbeats         HeartbeatCommitter
	recovery           RecoveryCommitter
	transcripts        TranscriptReader
	descriptions       sessiondescription.Service
	sessionFiles       SessionFileReader
	starts             sessionstart.Service
	providerAuth       providerauth.Service
	providerStops      providerstop.Service
	backendSetup       backendsetup.Service
	speechSetup        speechsetup.Service
	updates            clusterupdate.Service
	enrollments        EnrollmentCommitter
	enrollmentIssuerID string
	tlsConfig          *tls.Config
	httpServer         *http.Server
}

func NewServer(config ServerConfig) (*Server, error) {
	if config.NodeID == "" || config.ClusterID == "" || config.Leadership == nil ||
		config.Membership == nil || config.Service == nil {
		return nil, errors.New("node-control server dependencies are required")
	}
	if config.Enrollments != nil && config.EnrollmentIssuerID == "" {
		return nil, errors.New("enrollment issuer identity is required")
	}
	tlsConfig, err := serverTLSConfig(config.Certificate, config.Roots, config.ClusterID)
	if err != nil {
		return nil, err
	}
	server := &Server{
		nodeID: config.NodeID, clusterID: config.ClusterID, leadership: config.Leadership,
		membership: config.Membership, service: config.Service,
		health: config.Health, backups: config.Backups, admin: config.Admin,
		backupCertificate: config.Certificate,
		heartbeats:        config.Heartbeats, recovery: config.Recovery,
		transcripts: config.Transcripts, descriptions: config.Descriptions,
		sessionFiles: config.SessionFiles, starts: config.Starts,
		providerAuth: config.ProviderAuth, providerStops: config.ProviderStops,
		backendSetup: config.BackendSetup, tlsConfig: tlsConfig,
		speechSetup: config.SpeechSetup,
		updates:     config.Updates,
		enrollments: config.Enrollments, enrollmentIssuerID: config.EnrollmentIssuerID,
	}
	mux := http.NewServeMux()
	server.registerHandlers(mux)
	server.httpServer = &http.Server{
		Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second,
	}
	return server, nil
}

func (s *Server) Serve(listener net.Listener) error {
	if listener == nil {
		return errors.New("node-control listener is required")
	}
	err := s.httpServer.Serve(tls.NewListener(listener, s.tlsConfig))
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
