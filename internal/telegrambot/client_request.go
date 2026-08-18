package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (c *Client) call(
	ctx context.Context,
	method string,
	payload any,
	result any,
	timeout time.Duration,
) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode telegram %s payload: %w", method, err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		c.baseURL+"/bot"+c.token+"/"+method,
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("build telegram %s request: %w", method, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		if requestCtx.Err() != nil {
			return requestCtx.Err()
		}
		return &TransportError{Method: method, Cause: c.redactedTransportCause(err)}
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, c.maxResponseBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return &TransportError{Method: method, Cause: err}
	}
	if int64(len(responseBody)) > c.maxResponseBytes {
		return errors.New("telegram response exceeds configured limit")
	}
	outer := struct {
		OK          bool                  `json:"ok"`
		Result      json.RawMessage       `json:"result"`
		ErrorCode   int                   `json:"error_code,omitempty"`
		Description string                `json:"description,omitempty"`
		Parameters  apiResponseParameters `json:"parameters,omitempty"`
	}{}
	if err := json.Unmarshal(responseBody, &outer); err != nil {
		return errors.New("telegram returned malformed JSON")
	}
	if response.StatusCode != http.StatusOK || !outer.OK {
		return &APIError{
			Method: method, Code: outer.ErrorCode,
			Description: boundedDescription(strings.ReplaceAll(
				outer.Description, c.token, "[redacted]",
			)),
			RetryAfter: time.Duration(outer.Parameters.RetryAfter) * time.Second,
		}
	}
	if result == nil {
		return nil
	}
	if len(outer.Result) == 0 || string(outer.Result) == "null" {
		return errors.New("telegram response is missing a result")
	}
	if err := json.Unmarshal(outer.Result, result); err != nil {
		return errors.New("telegram returned a malformed result")
	}
	return nil
}

func boundedDescription(value string) string {
	const max = 256
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}
