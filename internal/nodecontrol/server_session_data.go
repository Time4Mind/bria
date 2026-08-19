package nodecontrol

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
)

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
