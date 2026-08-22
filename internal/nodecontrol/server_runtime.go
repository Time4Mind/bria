package nodecontrol

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/security"
)

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

func (s *Server) handleExecute(writer http.ResponseWriter, request *http.Request) {
	command, ok := s.authorizeAndDecode(writer, request)
	if !ok {
		return
	}
	receipt, err := s.service.Submit(request.Context(), command)
	if err != nil {
		if errors.Is(err, runtimehost.ErrQueueFull) {
			writer.Header().Set(runtimeErrorHeader, runtimeQueueFull)
			http.Error(writer, "runtime queue is full", http.StatusTooManyRequests)
			return
		}
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
