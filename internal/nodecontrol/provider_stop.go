package nodecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/providerstop"
)

const providerStopPath = "/v1/provider/stop"

func (c *Client) NotifyProviderStop(
	ctx context.Context,
	nodeID string,
	signal providerstop.Signal,
) error {
	if err := signal.Validate(); err != nil {
		return err
	}
	address, ok := c.resolver.ControlAddress(nodeID)
	if !ok {
		return fmt.Errorf("resolve control endpoint for %q", nodeID)
	}
	body, err := json.Marshal(signal)
	if err != nil || len(body) > maxControlPayload {
		return errors.New("invalid provider stop signal")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, "https://"+address+providerStopPath, bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client, err := c.clientFor(nodeID)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("notify provider stop: %w", err)
	}
	defer response.Body.Close()
	_, readErr := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if readErr != nil || response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("provider stop rejected with status %d", response.StatusCode)
	}
	return nil
}

func (s *Server) handleProviderStop(writer http.ResponseWriter, request *http.Request) {
	if s.providerStops == nil {
		http.Error(writer, "provider stop service unavailable", http.StatusServiceUnavailable)
		return
	}
	peerID, ok := s.authorizeMember(writer, request)
	if !ok {
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlPayload+1))
	decoder.DisallowUnknownFields()
	var signal providerstop.Signal
	if err := decoder.Decode(&signal); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		signal.Validate() != nil {
		http.Error(writer, "invalid provider stop signal", http.StatusBadRequest)
		return
	}
	// The originating provider runs under this node's Bria environment. A
	// follower forwards with the same node certificate, so the identity remains
	// verifiable at the leader without bearer tokens in hook configuration.
	if signal.NodeID != peerID {
		http.Error(writer, "provider stop source does not match peer", http.StatusForbidden)
		return
	}
	if err := s.providerStops.Notify(request.Context(), signal); err != nil {
		http.Error(writer, "provider stop unavailable", http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
