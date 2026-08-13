package nodecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/clusterupdate"
)

type UpdateClient struct{ client *Client }

func NewUpdateClient(client *Client) (*UpdateClient, error) {
	if client == nil {
		return nil, errors.New("node-control client is required")
	}
	return &UpdateClient{client: client}, nil
}

func (c *UpdateClient) Inspect(ctx context.Context) (clusterupdate.VerifiedManifest, error) {
	return clusterupdate.VerifiedManifest{}, errors.New("remote update inspection requires a node")
}

func (c *UpdateClient) InspectNode(
	ctx context.Context, nodeID string,
) (clusterupdate.VerifiedManifest, error) {
	var result clusterupdate.VerifiedManifest
	err := c.request(ctx, nodeID, updateInspectPath, nil, &result)
	return result, err
}

func (c *UpdateClient) Start(
	ctx context.Context, input clusterupdate.Request,
) (clusterupdate.Status, error) {
	var result clusterupdate.Status
	err := c.request(ctx, input.NodeID, updateStartPath, input, &result)
	return result, err
}

func (c *UpdateClient) Status(
	ctx context.Context, input clusterupdate.Request,
) (clusterupdate.Status, error) {
	var result clusterupdate.Status
	err := c.request(ctx, input.NodeID, updateStatusPath, input, &result)
	return result, err
}

func (c *UpdateClient) request(
	ctx context.Context, nodeID, path string, input any, output any,
) error {
	address, ok := c.client.resolver.ControlAddress(nodeID)
	if !ok {
		return fmt.Errorf("resolve control endpoint for %q", nodeID)
	}
	body := []byte(nil)
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, "https://"+address+path, bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client, err := c.client.clientFor(nodeID)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("node update request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if err != nil || len(responseBody) > maxControlPayload {
		return errors.New("invalid node update response")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("node update rejected with status %d", response.StatusCode)
	}
	if err := json.Unmarshal(responseBody, output); err != nil {
		return errors.New("malformed node update response")
	}
	return nil
}

var _ clusterupdate.Service = (*UpdateClient)(nil)
