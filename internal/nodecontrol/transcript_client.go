package nodecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/transcript"
)

const (
	transcriptPath       = "/v1/session/transcript"
	maxTranscriptPayload = 8 << 20
)

type transcriptResponse struct {
	Events []transcript.Event `json:"events"`
}

func (c *Client) ReadTranscript(
	ctx context.Context,
	query TranscriptQuery,
) ([]transcript.Event, error) {
	address, ok := c.resolver.ControlAddress(query.NodeID)
	if !ok {
		return nil, fmt.Errorf("resolve transcript endpoint for %q", query.NodeID)
	}
	body, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("encode transcript query: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, "https://"+address+transcriptPath, bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("build transcript query: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	client, err := c.clientFor(query.NodeID)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("read remote transcript: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxTranscriptPayload+1))
	if err != nil || len(responseBody) > maxTranscriptPayload {
		return nil, errors.New("invalid transcript response")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("transcript query rejected with status %d", response.StatusCode)
	}
	var result transcriptResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, errors.New("node returned a malformed transcript")
	}
	return result.Events, nil
}
