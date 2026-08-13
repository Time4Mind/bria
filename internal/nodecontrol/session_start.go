package nodecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Time4Mind/bria/internal/sessionstart"
	"github.com/Time4Mind/bria/internal/transcript"
)

const (
	startBrowsePath    = "/v1/session-start/browse"
	startDiscoverPath  = "/v1/session-start/discover"
	startProvisionPath = "/v1/session-start/provision"
)

func (s *Server) handleStartBrowse(writer http.ResponseWriter, request *http.Request) {
	var query sessionstart.BrowseRequest
	if !s.authorizeStart(writer, request, &query) {
		return
	}
	result, err := s.starts.Browse(request.Context(), query)
	s.writeStartResult(writer, result, err)
}

func (s *Server) handleStartDiscover(writer http.ResponseWriter, request *http.Request) {
	var query sessionstart.DiscoverRequest
	if !s.authorizeStart(writer, request, &query) {
		return
	}
	result, err := s.starts.Discover(request.Context(), query)
	s.writeStartResult(writer, result, err)
}

func (s *Server) handleStartProvision(writer http.ResponseWriter, request *http.Request) {
	var command sessionstart.ProvisionRequest
	if !s.authorizeStart(writer, request, &command) {
		return
	}
	if err := s.starts.Provision(request.Context(), command); err != nil {
		http.Error(writer, "session start rejected", http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) authorizeStart(writer http.ResponseWriter, request *http.Request, target any) bool {
	if s.starts == nil {
		http.Error(writer, "session start unavailable", http.StatusServiceUnavailable)
		return false
	}
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
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) writeStartResult(writer http.ResponseWriter, result any, err error) {
	if err != nil {
		http.Error(writer, "session start rejected", http.StatusConflict)
		return
	}
	encoded, encodeErr := json.Marshal(result)
	if encodeErr != nil || len(encoded) > maxControlPayload {
		http.Error(writer, "session start response too large", http.StatusRequestEntityTooLarge)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(encoded)
}

func (c *Client) Browse(ctx context.Context, request sessionstart.BrowseRequest) (sessionstart.BrowseResult, error) {
	var result sessionstart.BrowseResult
	err := c.postStart(ctx, string(request.NodeID), startBrowsePath, request, &result)
	return result, err
}

func (c *Client) Discover(ctx context.Context, request sessionstart.DiscoverRequest) (transcript.Discovery, error) {
	var result transcript.Discovery
	err := c.postStart(ctx, string(request.NodeID), startDiscoverPath, request, &result)
	return result, err
}

func (c *Client) Provision(ctx context.Context, request sessionstart.ProvisionRequest) error {
	return c.postStart(ctx, string(request.Session.NodeID), startProvisionPath, request, nil)
}

func (c *Client) postStart(ctx context.Context, nodeID, path string, payload, target any) error {
	address, ok := c.resolver.ControlAddress(nodeID)
	if !ok {
		return fmt.Errorf("resolve control endpoint for %q", nodeID)
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > maxControlPayload {
		return errors.New("invalid session start request")
	}
	requestCtx, cancel := context.WithTimeout(ctx, max(c.timeout, 2500*time.Millisecond))
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, "https://"+address+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	client, err := c.clientFor(nodeID)
	if err != nil {
		return err
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if err != nil || len(data) > maxControlPayload || response.StatusCode/100 != 2 {
		return errors.New("session start request rejected")
	}
	if target == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, target)
}
