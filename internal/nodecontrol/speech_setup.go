package nodecontrol

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/speechsetup"
)

const (
	speechSetupStartPath  = "/v1/speech-setup/start"
	speechSetupStatusPath = "/v1/speech-setup/status"
)

func (s *Server) handleSpeechSetupStart(writer http.ResponseWriter, request *http.Request) {
	if s.speechSetup == nil {
		http.Error(writer, "speech setup unavailable", http.StatusServiceUnavailable)
		return
	}
	var input speechsetup.Request
	if !s.authorizeSpeechSetupRequest(writer, request, &input) {
		return
	}
	status, err := s.speechSetup.Start(request.Context(), input)
	s.writeSpeechSetupStatus(writer, status, err)
}

func (s *Server) handleSpeechSetupStatus(writer http.ResponseWriter, request *http.Request) {
	if s.speechSetup == nil {
		http.Error(writer, "speech setup unavailable", http.StatusServiceUnavailable)
		return
	}
	var input speechsetup.Request
	if !s.authorizeSpeechSetupRequest(writer, request, &input) {
		return
	}
	status, err := s.speechSetup.Status(request.Context(), input)
	s.writeSpeechSetupStatus(writer, status, err)
}

func (s *Server) authorizeSpeechSetupRequest(
	writer http.ResponseWriter, request *http.Request, input *speechsetup.Request,
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
	if err := decoder.Decode(input); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		input.NodeID != s.nodeID {
		http.Error(writer, "invalid speech setup request", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) writeSpeechSetupStatus(
	writer http.ResponseWriter, status speechsetup.Status, err error,
) {
	if err != nil {
		http.Error(writer, "speech setup unavailable", http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(status)
}
