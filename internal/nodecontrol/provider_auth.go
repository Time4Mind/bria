package nodecontrol

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/providerauth"
)

const (
	providerAuthStartPath  = "/v1/provider-auth/start"
	providerAuthSubmitPath = "/v1/provider-auth/submit"
	providerAuthStatusPath = "/v1/provider-auth/status"
	providerAuthCancelPath = "/v1/provider-auth/cancel"
)

func (s *Server) handleProviderAuthStart(writer http.ResponseWriter, request *http.Request) {
	if s.providerAuth == nil {
		http.Error(writer, "provider authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	var input providerauth.StartRequest
	if !s.authorizeProviderAuthRequest(writer, request, &input, func() string { return input.NodeID }) {
		return
	}
	status, err := s.providerAuth.Start(request.Context(), input)
	s.writeProviderAuthStatus(writer, status, err)
}

func (s *Server) handleProviderAuthSubmit(writer http.ResponseWriter, request *http.Request) {
	if s.providerAuth == nil {
		http.Error(writer, "provider authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	var input providerauth.SubmitRequest
	if !s.authorizeProviderAuthRequest(writer, request, &input, func() string { return input.NodeID }) {
		return
	}
	status, err := s.providerAuth.Submit(request.Context(), input)
	s.writeProviderAuthStatus(writer, status, err)
}

func (s *Server) handleProviderAuthStatus(writer http.ResponseWriter, request *http.Request) {
	if s.providerAuth == nil {
		http.Error(writer, "provider authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	var input providerauth.FlowRequest
	if !s.authorizeProviderAuthRequest(writer, request, &input, func() string { return input.NodeID }) {
		return
	}
	status, err := s.providerAuth.Status(request.Context(), input)
	s.writeProviderAuthStatus(writer, status, err)
}

func (s *Server) handleProviderAuthCancel(writer http.ResponseWriter, request *http.Request) {
	if s.providerAuth == nil {
		http.Error(writer, "provider authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	var input providerauth.FlowRequest
	if !s.authorizeProviderAuthRequest(writer, request, &input, func() string { return input.NodeID }) {
		return
	}
	if err := s.providerAuth.Cancel(request.Context(), input); err != nil {
		http.Error(writer, "provider authentication unavailable", http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) authorizeProviderAuthRequest(
	writer http.ResponseWriter,
	request *http.Request,
	destination any,
	nodeID func() string,
) bool {
	peerID, ok := s.authorizeMember(writer, request)
	if !ok {
		return false
	}
	if leaderID := s.leadership.LeaderID(); leaderID == "" || leaderID != peerID {
		http.Error(writer, "not current leader", http.StatusConflict)
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlPayload+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return false
	}
	if nodeID() != s.nodeID {
		http.Error(writer, "wrong target", http.StatusConflict)
		return false
	}
	return true
}

func (s *Server) writeProviderAuthStatus(
	writer http.ResponseWriter,
	status providerauth.Status,
	err error,
) {
	if err != nil {
		http.Error(writer, "provider authentication unavailable", http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(status)
}
