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

	"github.com/Time4Mind/bria/internal/providerauth"
)

type ProviderAuthClient struct{ client *Client }

func NewProviderAuthClient(client *Client) (*ProviderAuthClient, error) {
	if client == nil {
		return nil, errors.New("node-control client is required")
	}
	return &ProviderAuthClient{client: client}, nil
}

func (c *ProviderAuthClient) Start(
	ctx context.Context,
	request providerauth.StartRequest,
) (providerauth.Status, error) {
	return c.providerAuthRequest(ctx, request.NodeID, providerAuthStartPath, request, 30*time.Second)
}

func (c *ProviderAuthClient) Submit(
	ctx context.Context,
	request providerauth.SubmitRequest,
) (providerauth.Status, error) {
	return c.providerAuthRequest(ctx, request.NodeID, providerAuthSubmitPath, request, c.client.timeout)
}

func (c *ProviderAuthClient) Status(
	ctx context.Context,
	request providerauth.FlowRequest,
) (providerauth.Status, error) {
	return c.providerAuthRequest(ctx, request.NodeID, providerAuthStatusPath, request, c.client.timeout)
}

func (c *ProviderAuthClient) Cancel(ctx context.Context, request providerauth.FlowRequest) error {
	_, err := c.providerAuthRequest(ctx, request.NodeID, providerAuthCancelPath, request, c.client.timeout)
	return err
}

func (c *ProviderAuthClient) providerAuthRequest(
	ctx context.Context,
	nodeID string,
	path string,
	requestBody any,
	timeout time.Duration,
) (providerauth.Status, error) {
	address, ok := c.client.resolver.ControlAddress(nodeID)
	if !ok {
		return providerauth.Status{}, fmt.Errorf("resolve control endpoint for %q", nodeID)
	}
	body, err := json.Marshal(requestBody)
	if err != nil || len(body) > maxControlPayload {
		return providerauth.Status{}, errors.New("invalid provider authentication request")
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, "https://"+address+path, bytes.NewReader(body),
	)
	if err != nil {
		return providerauth.Status{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	client, err := c.client.clientFor(nodeID)
	if err != nil {
		return providerauth.Status{}, err
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return providerauth.Status{}, fmt.Errorf("provider authentication request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if err != nil || len(responseBody) > maxControlPayload {
		return providerauth.Status{}, errors.New("invalid provider authentication response")
	}
	if path == providerAuthCancelPath && response.StatusCode == http.StatusNoContent {
		return providerauth.Status{}, nil
	}
	if response.StatusCode != http.StatusOK {
		return providerauth.Status{}, fmt.Errorf(
			"provider authentication request rejected with status %d", response.StatusCode,
		)
	}
	var status providerauth.Status
	if err := json.Unmarshal(responseBody, &status); err != nil || status.FlowID == "" {
		return providerauth.Status{}, errors.New("node returned malformed provider authentication state")
	}
	return status, nil
}

var _ providerauth.Service = (*ProviderAuthClient)(nil)
