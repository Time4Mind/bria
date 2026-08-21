package nodecontrol

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/sessiondescription"
)

func (s *Server) handleArchiveDescription(writer http.ResponseWriter, request *http.Request) {
	if s.descriptions == nil {
		http.Error(writer, "archive description service unavailable", http.StatusServiceUnavailable)
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
	var query sessiondescription.Request
	if err := decoder.Decode(&query); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if string(query.NodeID) != s.nodeID || string(query.Session.NodeID) != s.nodeID {
		http.Error(writer, "wrong target", http.StatusConflict)
		return
	}
	result, err := s.descriptions.Generate(request.Context(), query)
	if err != nil {
		http.Error(writer, "archive description unavailable", http.StatusConflict)
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxArchiveDescriptionPayload {
		http.Error(writer, "archive description exceeds response limit", http.StatusRequestEntityTooLarge)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
}
