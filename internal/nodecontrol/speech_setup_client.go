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

	"github.com/Time4Mind/bria/internal/speechsetup"
)

type SpeechSetupClient struct{ client *Client }

func NewSpeechSetupClient(client *Client) (*SpeechSetupClient, error) {
	if client == nil {
		return nil, errors.New("node-control client is required")
	}
	return &SpeechSetupClient{client: client}, nil
}

func (c *SpeechSetupClient) Start(
	ctx context.Context, request speechsetup.Request,
) (speechsetup.Status, error) {
	return c.request(ctx, speechSetupStartPath, request, 5*time.Second)
}

func (c *SpeechSetupClient) Status(
	ctx context.Context, request speechsetup.Request,
) (speechsetup.Status, error) {
	return c.request(ctx, speechSetupStatusPath, request, c.client.timeout)
}

func (c *SpeechSetupClient) request(
	ctx context.Context, path string, bodyValue speechsetup.Request, timeout time.Duration,
) (speechsetup.Status, error) {
	address, ok := c.client.resolver.ControlAddress(bodyValue.NodeID)
	if !ok {
		return speechsetup.Status{}, fmt.Errorf("resolve control endpoint for %q", bodyValue.NodeID)
	}
	body, err := json.Marshal(bodyValue)
	if err != nil {
		return speechsetup.Status{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, "https://"+address+path, bytes.NewReader(body),
	)
	if err != nil {
		return speechsetup.Status{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	client, err := c.client.clientFor(bodyValue.NodeID)
	if err != nil {
		return speechsetup.Status{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return speechsetup.Status{}, fmt.Errorf("speech setup request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if err != nil || len(responseBody) > maxControlPayload {
		return speechsetup.Status{}, errors.New("invalid speech setup response")
	}
	if response.StatusCode != http.StatusOK {
		return speechsetup.Status{}, fmt.Errorf("speech setup rejected with status %d", response.StatusCode)
	}
	var status speechsetup.Status
	if err := json.Unmarshal(responseBody, &status); err != nil || !status.Validate(bodyValue.NodeID) {
		return speechsetup.Status{}, errors.New("node returned malformed speech setup state")
	}
	return status, nil
}

var _ speechsetup.Service = (*SpeechSetupClient)(nil)
