package nodecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const heartbeatPath = "/v1/node/heartbeat"

func (c *Client) PublishHeartbeat(
	ctx context.Context,
	leaderID string,
	report Heartbeat,
) (HeartbeatAck, error) {
	address, ok := c.resolver.ControlAddress(leaderID)
	if !ok {
		return HeartbeatAck{}, fmt.Errorf("resolve leader control endpoint for %q", leaderID)
	}
	body, err := json.Marshal(report)
	if err != nil {
		return HeartbeatAck{}, fmt.Errorf("encode heartbeat: %w", err)
	}
	if len(body) > maxControlPayload {
		return HeartbeatAck{}, errors.New("heartbeat exceeds control payload limit")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, "https://"+address+heartbeatPath, bytes.NewReader(body),
	)
	if err != nil {
		return HeartbeatAck{}, fmt.Errorf("build heartbeat request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	client, err := c.clientFor(leaderID)
	if err != nil {
		return HeartbeatAck{}, err
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return HeartbeatAck{}, fmt.Errorf("publish heartbeat: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if err != nil || len(responseBody) > maxControlPayload {
		return HeartbeatAck{}, errors.New("invalid heartbeat response")
	}
	if response.StatusCode != http.StatusOK {
		return HeartbeatAck{}, fmt.Errorf("heartbeat rejected with status %d", response.StatusCode)
	}
	var ack HeartbeatAck
	if err := json.Unmarshal(responseBody, &ack); err != nil {
		return HeartbeatAck{}, errors.New("leader returned a malformed heartbeat response")
	}
	return ack, nil
}
