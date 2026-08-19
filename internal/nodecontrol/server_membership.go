package nodecontrol

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func (s *Server) handleMembershipRelocation(writer http.ResponseWriter, request *http.Request) {
	if s.admin == nil {
		http.Error(writer, "membership administration unavailable", http.StatusServiceUnavailable)
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
	var relocation MembershipRelocation
	if err := decoder.Decode(&relocation); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		!validBriaMembershipRelocation(relocation) {
		http.Error(writer, "invalid relocation", http.StatusBadRequest)
		return
	}
	command, err := clusterstate.NewCommand(
		"relocate-"+relocation.NodeID+"-"+time.Now().UTC().Format("20060102150405.000000000"),
		clusterstate.CommandUpdateNodeMetadata, time.Now(), domain.Node{
			ID: domain.NodeID(relocation.NodeID), Network: domain.NodeNetwork{
				RaftAddress: relocation.RaftAddress, ControlAddress: relocation.ControlAddress,
				EnrollmentAddress: relocation.EnrollmentAddress,
			},
		},
	)
	if err != nil {
		http.Error(writer, "invalid relocation command", http.StatusBadRequest)
		return
	}
	result, err := s.admin.Apply(request.Context(), command)
	if err != nil || result.Err() != nil {
		http.Error(writer, "relocation state rejected", http.StatusConflict)
		return
	}
	// The dynamic membership loop learns the new authenticated address from
	// the replicated state, updates the TLS resolver, and only then asks Raft
	// to move the member. Waiting for the exact committed address prevents a
	// successful API response from leaving state and membership divergent.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !s.admin.IsMemberAt(relocation.NodeID, relocation.RaftAddress) {
		select {
		case <-request.Context().Done():
			http.Error(writer, "member relocation timed out", http.StatusGatewayTimeout)
			return
		case <-ticker.C:
		}
	}
	writer.WriteHeader(http.StatusNoContent)
}

func validBriaMembershipRelocation(relocation MembershipRelocation) bool {
	if relocation.NodeID == "" {
		return false
	}
	for _, address := range []string{relocation.RaftAddress, relocation.ControlAddress} {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.HasSuffix(host, ".bria.internal") || port == "" {
			return false
		}
	}
	return true
}

func (s *Server) handleMembershipAdmin(writer http.ResponseWriter, request *http.Request) {
	if s.admin == nil {
		http.Error(writer, "membership administration unavailable", http.StatusServiceUnavailable)
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
	if err := decoder.Decode(&command); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if command.Kind != clusterstate.CommandSetNodeLifecycle &&
		command.Kind != clusterstate.CommandDeleteNode {
		http.Error(writer, "unsupported membership command", http.StatusForbidden)
		return
	}
	targetNodeID, waitForRemoval, err := retirementTarget(command)
	if err != nil || targetNodeID == "" || targetNodeID == s.nodeID {
		http.Error(writer, "invalid retirement command", http.StatusBadRequest)
		return
	}
	result, err := s.admin.Apply(request.Context(), command)
	if err != nil || result.Err() != nil {
		http.Error(writer, "membership command rejected", http.StatusConflict)
		return
	}
	if waitForRemoval {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for s.admin.IsMember(targetNodeID) {
			select {
			case <-request.Context().Done():
				http.Error(writer, "membership removal timed out", http.StatusGatewayTimeout)
				return
			case <-ticker.C:
			}
		}
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(result)
}

func retirementTarget(command clusterstate.Command) (string, bool, error) {
	switch command.Kind {
	case clusterstate.CommandSetNodeLifecycle:
		var payload clusterstate.SetNodeLifecycle
		if err := json.Unmarshal(command.Payload, &payload); err != nil ||
			payload.Lifecycle != domain.NodeDisabled {
			return "", false, errors.New("only disabling a node is allowed")
		}
		return string(payload.NodeID), true, nil
	case clusterstate.CommandDeleteNode:
		var payload clusterstate.DeleteNode
		if err := json.Unmarshal(command.Payload, &payload); err != nil {
			return "", false, err
		}
		return string(payload.NodeID), false, nil
	default:
		return "", false, errors.New("unsupported membership command")
	}
}
