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

const enrollmentReportPath = "/v1/cluster/enrollment"

func (c *Client) ReportEnrollment(
	ctx context.Context,
	leaderID string,
	report EnrollmentReport,
) error {
	address, ok := c.resolver.ControlAddress(leaderID)
	if !ok {
		return fmt.Errorf("resolve leader control endpoint for %q", leaderID)
	}
	body, err := json.Marshal(report)
	if err != nil || len(body) > maxControlPayload {
		return errors.New("enrollment report exceeds control payload limit")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+address+enrollmentReportPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client, err := c.clientFor(leaderID)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("report enrollment: %w", err)
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if readErr != nil || len(data) > maxControlPayload {
		return errors.New("invalid enrollment report response")
	}
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("enrollment report rejected with status %d", response.StatusCode)
	}
	return nil
}
