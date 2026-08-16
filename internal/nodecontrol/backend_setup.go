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

	"github.com/Time4Mind/bria/internal/backendsetup"
)

const (
	backendSetupStartPath  = "/v1/backend-setup/start"
	backendSetupStatusPath = "/v1/backend-setup/status"
)

func (s *Server) handleBackendSetupStart(writer http.ResponseWriter, request *http.Request) {
	s.handleBackendSetup(writer, request, true)
}

func (s *Server) handleBackendSetupStatus(writer http.ResponseWriter, request *http.Request) {
	s.handleBackendSetup(writer, request, false)
}

func (s *Server) handleBackendSetup(
	writer http.ResponseWriter, request *http.Request, start bool,
) {
	if s.backendSetup == nil {
		http.Error(writer, "backend setup unavailable", http.StatusServiceUnavailable)
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
	var input backendsetup.Request
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlPayload+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		input.NodeID != s.nodeID {
		http.Error(writer, "invalid backend setup request", http.StatusBadRequest)
		return
	}
	var status backendsetup.Status
	var err error
	if start {
		status, err = s.backendSetup.Start(request.Context(), input)
	} else {
		status, err = s.backendSetup.Status(request.Context(), input)
	}
	if err != nil {
		http.Error(writer, "backend setup unavailable", http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(status)
}

type BackendSetupClient struct{ client *Client }

func NewBackendSetupClient(client *Client) (*BackendSetupClient, error) {
	if client == nil {
		return nil, errors.New("node-control client is required")
	}
	return &BackendSetupClient{client: client}, nil
}

func (c *BackendSetupClient) Start(
	ctx context.Context, request backendsetup.Request,
) (backendsetup.Status, error) {
	return c.request(ctx, backendSetupStartPath, request, 5*time.Second)
}

func (c *BackendSetupClient) Status(
	ctx context.Context, request backendsetup.Request,
) (backendsetup.Status, error) {
	return c.request(ctx, backendSetupStatusPath, request, c.client.timeout)
}

func (c *BackendSetupClient) request(
	ctx context.Context, path string, value backendsetup.Request, timeout time.Duration,
) (backendsetup.Status, error) {
	address, ok := c.client.resolver.ControlAddress(value.NodeID)
	if !ok {
		return backendsetup.Status{}, fmt.Errorf("resolve control endpoint for %q", value.NodeID)
	}
	body, err := json.Marshal(value)
	if err != nil {
		return backendsetup.Status{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, "https://"+address+path, bytes.NewReader(body),
	)
	if err != nil {
		return backendsetup.Status{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	client, err := c.client.clientFor(value.NodeID)
	if err != nil {
		return backendsetup.Status{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return backendsetup.Status{}, fmt.Errorf("backend setup request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if err != nil || len(responseBody) > maxControlPayload {
		return backendsetup.Status{}, errors.New("invalid backend setup response")
	}
	if response.StatusCode != http.StatusOK {
		return backendsetup.Status{}, fmt.Errorf("backend setup rejected with status %d", response.StatusCode)
	}
	var status backendsetup.Status
	if json.Unmarshal(responseBody, &status) != nil || !status.Validate(value) {
		return backendsetup.Status{}, errors.New("node returned malformed backend setup state")
	}
	return status, nil
}

var _ backendsetup.Service = (*BackendSetupClient)(nil)
