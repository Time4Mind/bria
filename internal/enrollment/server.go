package enrollment

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

type StateReader interface{ State() *domain.State }

type Submitter interface {
	SubmitEnrollment(context.Context, domain.EnrollmentRequest, string) error
}

type ServerConfig struct {
	ClusterID         string
	IssuerNodeID      string
	EnrollmentAddress string
	Certificate       tls.Certificate
	CA                security.CertificateAuthority
	CAPEM             []byte
	CallbackKey       []byte
	UpdateManifestURL string
	UpdatePublicKey   string
	State             StateReader
	Submit            Submitter
	Now               func() time.Time
}

type Server struct {
	config ServerConfig
	http   *http.Server
}

func NewServer(config ServerConfig) (*Server, error) {
	if config.ClusterID == "" || config.IssuerNodeID == "" || config.EnrollmentAddress == "" ||
		len(config.Certificate.Certificate) == 0 || config.CA.Certificate == nil ||
		len(config.CAPEM) == 0 || len(config.CallbackKey) == 0 || config.State == nil ||
		config.Submit == nil {
		return nil, errors.New("enrollment server configuration is incomplete")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	server := &Server{config: config}
	mux := http.NewServeMux()
	mux.HandleFunc(registerPath, server.handleRegister)
	mux.HandleFunc(statusPath, server.handleStatus)
	server.http = &http.Server{
		Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 5 * time.Second, IdleTimeout: 15 * time.Second,
	}
	return server, nil
}

func (s *Server) Serve(listener net.Listener) error {
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{s.config.Certificate}, MinVersion: tls.VersionTLS13,
	}
	err := s.http.Serve(tls.NewListener(listener, tlsConfig))
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *Server) handleRegister(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	var input RegisterRequest
	if !decodeJSON(request, &input) {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	state := s.config.State.State()
	invite, ok := state.EnrollmentInvites[input.TokenID]
	if !ok || invite.ValidateSecret(input.Secret, s.config.Now()) != nil {
		http.Error(writer, "invitation rejected", http.StatusForbidden)
		return
	}
	contract, err := security.DecodeNodeContract(input.Contract, s.config.Now())
	if err != nil {
		http.Error(writer, "contract rejected", http.StatusForbidden)
		return
	}
	enrollmentRequest := contract.EnrollmentRequest(s.config.Now())
	enrollmentRequest.InviteID = input.TokenID
	if err := s.config.Submit.SubmitEnrollment(
		request.Context(), enrollmentRequest, invite.SecretHash,
	); err != nil {
		http.Error(writer, "enrollment unavailable", http.StatusConflict)
		return
	}
	writeJSON(writer, http.StatusAccepted, RegisterResponse{
		RequestID: enrollmentRequest.ID, ExpiresAt: enrollmentRequest.ExpiresAt,
	})
}

func (s *Server) handleStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	var input StatusRequest
	if !decodeJSON(request, &input) {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	state := s.config.State.State()
	pending, ok := state.EnrollmentRequests[input.RequestID]
	if !ok {
		http.Error(writer, "unknown request", http.StatusNotFound)
		return
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(pending.PublicKey)
	now := s.config.Now()
	if err != nil || VerifyStatusRequest(input, ed25519.PublicKey(publicKey), now) != nil {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !now.Before(pending.ExpiresAt) {
		http.Error(writer, "enrollment request expired", http.StatusGone)
		return
	}
	response := StatusResponse{Status: pending.Status}
	if pending.Status == domain.EnrollmentApproved {
		node, exists := state.Nodes[pending.NodeID]
		if !exists || !node.Enabled() {
			http.Error(writer, "enrollment identity is no longer active", http.StatusGone)
			return
		}
		bundle, err := s.approvedBundle(state, pending, ed25519.PublicKey(publicKey))
		if err != nil {
			http.Error(writer, "issuance unavailable", http.StatusConflict)
			return
		}
		response.Bundle = &bundle
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) approvedBundle(
	state *domain.State,
	request domain.EnrollmentRequest,
	publicKey ed25519.PublicKey,
) (ApprovedBundle, error) {
	certificate, err := security.IssueNodeCertificateForPublicKey(
		s.config.CA, s.config.ClusterID, string(request.NodeID), publicKey,
		s.config.Now(), 365*24*time.Hour,
	)
	if err != nil {
		return ApprovedBundle{}, err
	}
	peers := make([]Peer, 0, len(state.Nodes))
	for _, node := range state.Nodes {
		if node.Enabled() && node.Network.RaftAddress != "" {
			peers = append(peers, Peer{NodeID: string(node.ID), Name: node.Name,
				RaftAddress: node.Network.RaftAddress, ControlAddress: node.Network.ControlAddress})
		}
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].NodeID < peers[j].NodeID })
	return ApprovedBundle{
		ClusterID: s.config.ClusterID, IssuerNodeID: s.config.IssuerNodeID,
		EnrollmentAddress: s.config.EnrollmentAddress,
		Certificate:       string(certificate), CACertificate: string(s.config.CAPEM),
		CallbackKey: base64.RawURLEncoding.EncodeToString(s.config.CallbackKey), Peers: peers,
		UpdateManifestURL: s.config.UpdateManifestURL, UpdatePublicKey: s.config.UpdatePublicKey,
	}, nil
}

func decodeJSON(request *http.Request, target any) bool {
	if request.Header.Get("Content-Type") != "application/json" {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, maxPayload+1))
	if err != nil || len(data) > maxPayload {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
