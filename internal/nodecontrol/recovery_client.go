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

const recoveryPath = "/v1/node/recovery"

func (c *Client) ReportRecovery(
	ctx context.Context,
	leaderID string,
	report RecoveryReport,
) error {
	address, ok := c.resolver.ControlAddress(leaderID)
	if !ok {
		return fmt.Errorf("resolve leader control endpoint for %q", leaderID)
	}
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode recovery report: %w", err)
	}
	if len(body) > maxControlPayload {
		return errors.New("recovery report exceeds control payload limit")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, "https://"+address+recoveryPath, bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("build recovery request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	client, err := c.clientFor(leaderID)
	if err != nil {
		return err
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("report recovery: %w", err)
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if readErr != nil || len(responseBody) > maxControlPayload {
		return errors.New("invalid recovery response")
	}
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("recovery report rejected with status %d", response.StatusCode)
	}
	return nil
}
