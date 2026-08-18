package nodecontrol

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/backendsetup"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerauth"
	"github.com/Time4Mind/bria/internal/providerstop"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/security"
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
		transcripts: config.Transcripts, sessionFiles: config.SessionFiles, starts: config.Starts,
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

func (s *Server) handleMembershipRelocation(writer http.ResponseWriter, request *http.Request) {
	if s.admin == nil {
		http.Error(writer, "membership administration unavailable", http.StatusServiceUnavailable)
		return
	}
	peerID, ok := s.authorizeMember(writer, request)
	if !ok {
		return
	}
	if peerID != s.nodeID || s.admin.LeaderID() != s.nodeID {
		http.Error(writer, "local leader identity required", http.StatusForbidden)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlPayload+1))
	decoder.DisallowUnknownFields()
	var relocation MembershipRelocation
	if err := decoder.Decode(&relocation); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		!validBriaMembershipRelocation(relocation) {
		http.Error(writer, "invalid relocation", http.StatusBadRequest)
		return
	}
	command, err := clusterstate.NewCommand(
		"relocate-"+relocation.NodeID+"-"+time.Now().UTC().Format("20060102150405.000000000"),
		clusterstate.CommandUpdateNodeMetadata, time.Now(), domain.Node{
			ID: domain.NodeID(relocation.NodeID), Network: domain.NodeNetwork{
				RaftAddress: relocation.RaftAddress, ControlAddress: relocation.ControlAddress,
				EnrollmentAddress: relocation.EnrollmentAddress,
			},
		},
	)
	if err != nil {
		http.Error(writer, "invalid relocation command", http.StatusBadRequest)
		return
	}
	result, err := s.admin.Apply(request.Context(), command)
	if err != nil || result.Err() != nil {
		http.Error(writer, "relocation state rejected", http.StatusConflict)
		return
	}
	// The dynamic membership loop learns the new authenticated address from
	// the replicated state, updates the TLS resolver, and only then asks Raft
	// to move the member. Waiting for the exact committed address prevents a
	// successful API response from leaving state and membership divergent.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !s.admin.IsMemberAt(relocation.NodeID, relocation.RaftAddress) {
		select {
		case <-request.Context().Done():
			http.Error(writer, "member relocation timed out", http.StatusGatewayTimeout)
			return
		case <-ticker.C:
		}
	}
	writer.WriteHeader(http.StatusNoContent)
}

func validBriaMembershipRelocation(relocation MembershipRelocation) bool {
	if relocation.NodeID == "" {
		return false
	}
	for _, address := range []string{relocation.RaftAddress, relocation.ControlAddress} {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.HasSuffix(host, ".bria.internal") || port == "" {
			return false
		}
	}
	return true
}

func (s *Server) handleMembershipAdmin(writer http.ResponseWriter, request *http.Request) {
	if s.admin == nil {
		http.Error(writer, "membership administration unavailable", http.StatusServiceUnavailable)
		return
	}
	peerID, ok := s.authorizeMember(writer, request)
	if !ok {
		return
	}
	if peerID != s.nodeID || s.admin.LeaderID() != s.nodeID {
		http.Error(writer, "local leader identity required", http.StatusForbidden)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlPayload+1))
	decoder.DisallowUnknownFields()
	var command clusterstate.Command
	if err := decoder.Decode(&command); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if command.Kind != clusterstate.CommandSetNodeLifecycle &&
		command.Kind != clusterstate.CommandDeleteNode {
		http.Error(writer, "unsupported membership command", http.StatusForbidden)
		return
	}
	targetNodeID, waitForRemoval, err := retirementTarget(command)
	if err != nil || targetNodeID == "" || targetNodeID == s.nodeID {
		http.Error(writer, "invalid retirement command", http.StatusBadRequest)
		return
	}
	result, err := s.admin.Apply(request.Context(), command)
	if err != nil || result.Err() != nil {
		http.Error(writer, "membership command rejected", http.StatusConflict)
		return
	}
	if waitForRemoval {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for s.admin.IsMember(targetNodeID) {
			select {
			case <-request.Context().Done():
				http.Error(writer, "membership removal timed out", http.StatusGatewayTimeout)
				return
			case <-ticker.C:
			}
		}
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(result)
}

func retirementTarget(command clusterstate.Command) (string, bool, error) {
	switch command.Kind {
	case clusterstate.CommandSetNodeLifecycle:
		var payload clusterstate.SetNodeLifecycle
		if err := json.Unmarshal(command.Payload, &payload); err != nil ||
			payload.Lifecycle != domain.NodeDisabled {
			return "", false, errors.New("only disabling a node is allowed")
		}
		return string(payload.NodeID), true, nil
	case clusterstate.CommandDeleteNode:
		var payload clusterstate.DeleteNode
		if err := json.Unmarshal(command.Payload, &payload); err != nil {
			return "", false, err
		}
		return string(payload.NodeID), false, nil
	default:
		return "", false, errors.New("unsupported membership command")
	}
}

func (s *Server) handleTranscript(writer http.ResponseWriter, request *http.Request) {
	if s.transcripts == nil {
		http.Error(writer, "transcript service unavailable", http.StatusServiceUnavailable)
		return
	}
	peerID, ok := s.authorizeMember(writer, request)
	if !ok {
		return
	}
	if leaderID := s.leadership.LeaderID(); leaderID == "" || leaderID != peerID {
		http.Error(writer, "not current leader", http.StatusConflict)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlPayload+1))
	decoder.DisallowUnknownFields()
	var query TranscriptQuery
	if err := decoder.Decode(&query); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if query.NodeID != s.nodeID {
		http.Error(writer, "wrong target", http.StatusConflict)
		return
	}
	events, err := s.transcripts.ReadTranscript(request.Context(), query)
	if err != nil {
		http.Error(writer, "transcript unavailable", http.StatusConflict)
		return
	}
	encoded, err := json.Marshal(transcriptResponse{Events: events})
	if err != nil || len(encoded) > maxTranscriptPayload {
		http.Error(writer, "transcript exceeds response limit", http.StatusRequestEntityTooLarge)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
}

func (s *Server) handleSessionFile(writer http.ResponseWriter, request *http.Request) {
	if s.sessionFiles == nil {
		http.Error(writer, "session file service unavailable", http.StatusServiceUnavailable)
		return
	}
	peerID, ok := s.authorizeMember(writer, request)
	if !ok {
		return
	}
	if leaderID := s.leadership.LeaderID(); leaderID == "" || leaderID != peerID {
		http.Error(writer, "not current leader", http.StatusConflict)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlPayload+1))
	decoder.DisallowUnknownFields()
	var query SessionFileQuery
	if err := decoder.Decode(&query); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if query.NodeID != s.nodeID {
		http.Error(writer, "wrong target", http.StatusConflict)
		return
	}
	file, err := s.sessionFiles.OpenSessionFile(request.Context(), query)
	if err != nil {
		http.Error(writer, "session file unavailable", http.StatusNotFound)
		return
	}
	defer file.Content.Close()
	writer.Header().Set("Content-Type", file.MIMEType)
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": file.Name,
	}))
	writer.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	writer.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(writer, file.Content, file.Size)
}

func (s *Server) handleRecovery(writer http.ResponseWriter, request *http.Request) {
	if s.recovery == nil {
		http.Error(writer, "recovery service unavailable", http.StatusServiceUnavailable)
		return
	}
	peerID, ok := s.authorizeMember(writer, request)
	if !ok {
		return
	}
	if leaderID := s.leadership.LeaderID(); leaderID == "" || leaderID != s.nodeID {
		http.Error(writer, "not current leader", http.StatusConflict)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlPayload+1))
	decoder.DisallowUnknownFields()
	var report RecoveryReport
	if err := decoder.Decode(&report); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if report.NodeID != peerID || report.Session.NodeID != domain.NodeID(peerID) {
		http.Error(writer, "node identity mismatch", http.StatusForbidden)
		return
	}
	if err := s.recovery.CommitRecovery(request.Context(), report); err != nil {
		http.Error(writer, "recovery report rejected", http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHeartbeat(writer http.ResponseWriter, request *http.Request) {
	if s.heartbeats == nil {
		http.Error(writer, "heartbeat service unavailable", http.StatusServiceUnavailable)
		return
	}
	peerID, certificate, ok := s.authorizeMemberCertificate(writer, request)
	if !ok {
		return
	}
	if leaderID := s.leadership.LeaderID(); leaderID == "" || leaderID != s.nodeID {
		http.Error(writer, "not current leader", http.StatusConflict)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlPayload+1))
	decoder.DisallowUnknownFields()
	var report Heartbeat
	if err := decoder.Decode(&report); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if report.NodeID != peerID {
		http.Error(writer, "node identity mismatch", http.StatusForbidden)
		return
	}
	fingerprint, err := security.NodeCertificateFingerprint(certificate)
	if err != nil {
		http.Error(writer, "invalid member identity", http.StatusForbidden)
		return
	}
	previous, present, err := security.PreviousNodeCertificateFingerprint(certificate)
	if err != nil {
		http.Error(writer, "invalid member identity", http.StatusForbidden)
		return
	}
	report.CertificateFingerprint = fingerprint
	if present {
		report.PreviousCertificateFingerprint = previous
	} else {
		report.PreviousCertificateFingerprint = ""
	}
	if err := validateHeartbeat(report); err != nil {
		http.Error(writer, "invalid heartbeat", http.StatusBadRequest)
		return
	}
	ack, err := s.heartbeats.CommitHeartbeat(request.Context(), report)
	if err != nil {
		http.Error(writer, "heartbeat rejected", http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(ack)
}

func (s *Server) handleLookup(writer http.ResponseWriter, request *http.Request) {
	command, ok := s.authorizeAndDecode(writer, request)
	if !ok {
		return
	}
	result, found, lookupErr := s.service.LookupResult(request.Context(), command)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(lookupResponse{
		Found: found, Failed: lookupErr != nil, Result: result,
	})
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

func (s *Server) handleExecute(writer http.ResponseWriter, request *http.Request) {
	command, ok := s.authorizeAndDecode(writer, request)
	if !ok {
		return
	}
	receipt, err := s.service.Submit(request.Context(), command)
	if err != nil {
		http.Error(writer, "runtime command rejected", http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(writer).Encode(receipt)
}

func (s *Server) authorizeAndDecode(
	writer http.ResponseWriter,
	request *http.Request,
) (runtimehost.Request, bool) {
	peerID, ok := s.authorizeMember(writer, request)
	if !ok {
		return runtimehost.Request{}, false
	}
	if leaderID := s.leadership.LeaderID(); leaderID == "" || leaderID != peerID {
		http.Error(writer, "not current leader", http.StatusConflict)
		return runtimehost.Request{}, false
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlPayload+1))
	decoder.DisallowUnknownFields()
	var command runtimehost.Request
	if err := decoder.Decode(&command); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return runtimehost.Request{}, false
	}
	if command.NodeID != s.nodeID {
		http.Error(writer, "wrong target", http.StatusConflict)
		return runtimehost.Request{}, false
	}
	return command, true
}
