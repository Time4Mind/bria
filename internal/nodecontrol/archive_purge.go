package nodecontrol

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func (s *Server) handleArchivePurge(writer http.ResponseWriter, request *http.Request) {
	if s.admin == nil {
		http.Error(writer, "archive administration unavailable", http.StatusServiceUnavailable)
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
	if err := decoder.Decode(&command); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		command.Kind != clusterstate.CommandPurgeSession {
		http.Error(writer, "invalid archive purge command", http.StatusBadRequest)
		return
	}
	var payload clusterstate.PurgeSession
	if err := json.Unmarshal(command.Payload, &payload); err != nil ||
		payload.Session.NodeID != domain.NodeID(s.nodeID) || payload.ExpectedRevision == 0 {
		http.Error(writer, "invalid archive purge target", http.StatusBadRequest)
		return
	}
	result, err := s.admin.Apply(request.Context(), command)
	if err != nil || result.Err() != nil {
		http.Error(writer, "archive purge rejected", http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(result)
}
