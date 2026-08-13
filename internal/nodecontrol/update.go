package nodecontrol

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/clusterupdate"
)

const (
	updateInspectPath = "/v1/update/inspect"
	updateStartPath   = "/v1/update/start"
	updateStatusPath  = "/v1/update/status"
)

func (s *Server) registerUpdateHandlers(mux *http.ServeMux) {
	mux.HandleFunc(updateInspectPath, s.handleUpdateInspect)
	mux.HandleFunc(updateStartPath, s.handleUpdateStart)
	mux.HandleFunc(updateStatusPath, s.handleUpdateStatus)
}

func (s *Server) handleUpdateInspect(writer http.ResponseWriter, request *http.Request) {
	if s.updates == nil {
		http.Error(writer, "cluster updates unavailable", http.StatusServiceUnavailable)
		return
	}
	if !s.authorizeUpdateRequest(writer, request, nil) {
		return
	}
	manifest, err := s.updates.Inspect(request.Context())
	if err != nil {
		http.Error(writer, "update manifest unavailable", http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(manifest)
}

func (s *Server) handleUpdateStart(writer http.ResponseWriter, request *http.Request) {
	if s.updates == nil {
		http.Error(writer, "cluster updates unavailable", http.StatusServiceUnavailable)
		return
	}
	var input clusterupdate.Request
	if !s.authorizeUpdateRequest(writer, request, &input) {
		return
	}
	status, err := s.updates.Start(request.Context(), input)
	s.writeUpdateStatus(writer, status, err)
}

func (s *Server) handleUpdateStatus(writer http.ResponseWriter, request *http.Request) {
	if s.updates == nil {
		http.Error(writer, "cluster updates unavailable", http.StatusServiceUnavailable)
		return
	}
	var input clusterupdate.Request
	if !s.authorizeUpdateRequest(writer, request, &input) {
		return
	}
	status, err := s.updates.Status(request.Context(), input)
	s.writeUpdateStatus(writer, status, err)
}

func (s *Server) authorizeUpdateRequest(
	writer http.ResponseWriter, request *http.Request, input *clusterupdate.Request,
) bool {
	peerID, ok := s.authorizeMember(writer, request)
	if !ok {
		return false
	}
	if leaderID := s.leadership.LeaderID(); leaderID == "" || leaderID != peerID {
		http.Error(writer, "not current leader", http.StatusConflict)
		return false
	}
	if input == nil {
		return true
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlPayload+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		input.NodeID != s.nodeID {
		http.Error(writer, "invalid update request", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) writeUpdateStatus(
	writer http.ResponseWriter, status clusterupdate.Status, err error,
) {
	if err != nil {
		http.Error(writer, "cluster update rejected", http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(status)
}
