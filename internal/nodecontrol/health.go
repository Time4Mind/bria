package nodecontrol

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	healthPath    = "/healthz"
	readinessPath = "/readyz"
	metricsPath   = "/metrics"
)

type HealthStatus struct {
	Status       string `json:"status"`
	NodeID       string `json:"node_id"`
	LeaderID     string `json:"leader_id,omitempty"`
	RaftState    string `json:"raft_state,omitempty"`
	AppliedIndex string `json:"applied_index,omitempty"`
}

func (s *Server) handleMetrics(writer http.ResponseWriter, request *http.Request) {
	if _, ok := s.authorizeHealthMember(writer, request); !ok {
		return
	}
	leaderID := ""
	stats := map[string]string{}
	if s.health != nil {
		leaderID = s.health.LeaderID()
		stats = s.health.Stats()
	}
	ready := leaderID != "" && s.membership.IsMember(s.nodeID)
	state := normalizedRaftState(stats["state"])
	appliedIndex, _ := strconv.ParseUint(stats["applied_index"], 10, 64)
	label := prometheusLabel(s.nodeID)
	body := fmt.Sprintf(`# HELP bria_process_up Whether the Bria node process is serving authenticated control traffic.
# TYPE bria_process_up gauge
bria_process_up{node_id="%s"} 1
# HELP bria_node_ready Whether the node is a member and observes a current Raft leader.
# TYPE bria_node_ready gauge
bria_node_ready{node_id="%s"} %d
# HELP bria_raft_has_leader Whether the node currently observes a Raft leader.
# TYPE bria_raft_has_leader gauge
bria_raft_has_leader{node_id="%s"} %d
# HELP bria_raft_applied_index Latest Raft log index applied by this node.
# TYPE bria_raft_applied_index gauge
bria_raft_applied_index{node_id="%s"} %d
# HELP bria_raft_state Current local Raft state as a one-hot label.
# TYPE bria_raft_state gauge
bria_raft_state{node_id="%s",state="%s"} 1
`, label, label, boolMetric(ready), label, boolMetric(leaderID != ""),
		label, appliedIndex, label, state)
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(body))
}

func normalizedRaftState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "leader", "follower", "candidate", "shutdown":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func prometheusLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Server) handleHealth(writer http.ResponseWriter, request *http.Request) {
	if _, ok := s.authorizeHealthMember(writer, request); !ok {
		return
	}
	s.writeHealth(writer, http.StatusOK, "ok")
}

func (s *Server) handleReadiness(writer http.ResponseWriter, request *http.Request) {
	if _, ok := s.authorizeHealthMember(writer, request); !ok {
		return
	}
	if s.health == nil || !s.membership.IsMember(s.nodeID) || s.health.LeaderID() == "" {
		s.writeHealth(writer, http.StatusServiceUnavailable, "not_ready")
		return
	}
	s.writeHealth(writer, http.StatusOK, "ready")
}

func (s *Server) authorizeHealthMember(
	writer http.ResponseWriter,
	request *http.Request,
) (string, bool) {
	if request.Method != http.MethodGet {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return "", false
	}
	peerID, certificate, err := s.authenticatedPeer(request)
	if err != nil {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return "", false
	}
	if !s.membership.IsMember(peerID) {
		http.Error(writer, "unknown member", http.StatusForbidden)
		return "", false
	}
	if membership, ok := s.membership.(CertificateMembership); ok &&
		!membership.AuthorizeCertificate(peerID, certificate) {
		http.Error(writer, "revoked member identity", http.StatusForbidden)
		return "", false
	}
	return peerID, true
}

func (s *Server) writeHealth(writer http.ResponseWriter, status int, value string) {
	response := HealthStatus{Status: value, NodeID: s.nodeID}
	if s.health != nil {
		response.LeaderID = s.health.LeaderID()
		stats := s.health.Stats()
		response.RaftState = stats["state"]
		response.AppliedIndex = stats["applied_index"]
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(response)
}
