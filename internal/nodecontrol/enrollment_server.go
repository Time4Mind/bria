package nodecontrol

import (
	"encoding/json"
	"io"
	"net/http"
)

func (s *Server) handleEnrollment(writer http.ResponseWriter, request *http.Request) {
	if s.enrollments == nil {
		http.Error(writer, "enrollment service unavailable", http.StatusServiceUnavailable)
		return
	}
	peerID, ok := s.authorizeMember(writer, request)
	if !ok {
		return
	}
	if peerID != s.enrollmentIssuerID {
		http.Error(writer, "not enrollment issuer", http.StatusForbidden)
		return
	}
	if s.leadership.LeaderID() != s.nodeID {
		http.Error(writer, "not current leader", http.StatusConflict)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlPayload+1))
	decoder.DisallowUnknownFields()
	var report EnrollmentReport
	if decoder.Decode(&report) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.enrollments.CommitEnrollment(request.Context(), report); err != nil {
		http.Error(writer, "enrollment rejected", http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
