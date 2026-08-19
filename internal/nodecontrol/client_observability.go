package nodecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Probe checks the authenticated process or quorum readiness of a cluster node.
func (c *Client) Probe(
	ctx context.Context,
	nodeID string,
	readiness bool,
) (HealthStatus, error) {
	address, ok := c.resolver.ControlAddress(nodeID)
	if !ok {
		return HealthStatus{}, fmt.Errorf("resolve control endpoint for %q", nodeID)
	}
	path := healthPath
	if readiness {
		path = readinessPath
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodGet, "https://"+address+path, nil,
	)
	if err != nil {
		return HealthStatus{}, fmt.Errorf("build health request: %w", err)
	}
	client, err := c.clientFor(nodeID)
	if err != nil {
		return HealthStatus{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return HealthStatus{}, fmt.Errorf("probe node: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if err != nil || len(body) > maxControlPayload {
		return HealthStatus{}, errors.New("invalid health response")
	}
	var status HealthStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return HealthStatus{}, errors.New("node returned malformed health status")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusServiceUnavailable {
		return HealthStatus{}, fmt.Errorf("health probe rejected with status %d", response.StatusCode)
	}
	return status, nil
}

func (c *Client) Metrics(ctx context.Context, nodeID string) (string, error) {
	address, ok := c.resolver.ControlAddress(nodeID)
	if !ok {
		return "", fmt.Errorf("resolve control endpoint for %q", nodeID)
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodGet, "https://"+address+metricsPath, nil,
	)
	if err != nil {
		return "", fmt.Errorf("build metrics request: %w", err)
	}
	client, err := c.clientFor(nodeID)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("read node metrics: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if err != nil || len(body) > maxControlPayload {
		return "", errors.New("invalid metrics response")
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metrics request rejected with status %d", response.StatusCode)
	}
	return string(body), nil
}
