package nodecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/runtimehost"
)

func (c *Client) Submit(
	ctx context.Context,
	request runtimehost.Request,
) (runtimehost.Receipt, error) {
	address, ok := c.resolver.ControlAddress(request.NodeID)
	if !ok {
		return runtimehost.Receipt{}, fmt.Errorf("resolve control endpoint for %q", request.NodeID)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return runtimehost.Receipt{}, fmt.Errorf("encode runtime request: %w", err)
	}
	if len(body) > maxControlPayload {
		return runtimehost.Receipt{}, errors.New("runtime request exceeds control payload limit")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, "https://"+address+executePath, bytes.NewReader(body),
	)
	if err != nil {
		return runtimehost.Receipt{}, fmt.Errorf("build runtime request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	client, err := c.clientFor(request.NodeID)
	if err != nil {
		return runtimehost.Receipt{}, err
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return runtimehost.Receipt{}, fmt.Errorf("submit runtime request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if err != nil {
		return runtimehost.Receipt{}, fmt.Errorf("read runtime response: %w", err)
	}
	if len(responseBody) > maxControlPayload {
		return runtimehost.Receipt{}, errors.New("runtime response exceeds control payload limit")
	}
	if err := runtimeSubmitResponseError(response); err != nil {
		return runtimehost.Receipt{}, err
	}
	var receipt runtimehost.Receipt
	if err := json.Unmarshal(responseBody, &receipt); err != nil {
		return runtimehost.Receipt{}, errors.New("node returned a malformed runtime receipt")
	}
	if receipt.OperationID != request.OperationID || !receipt.Accepted {
		return runtimehost.Receipt{}, errors.New("node returned an invalid runtime receipt")
	}
	return receipt, nil
}

func runtimeSubmitResponseError(response *http.Response) error {
	if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusOK {
		return nil
	}
	if response.StatusCode == http.StatusTooManyRequests &&
		response.Header.Get(runtimeErrorHeader) == runtimeQueueFull {
		return runtimehost.ErrQueueFull
	}
	return fmt.Errorf("runtime request rejected with status %d", response.StatusCode)
}

func (c *Client) LookupResult(
	ctx context.Context,
	request runtimehost.Request,
) (runtimehost.Result, bool, error) {
	address, ok := c.resolver.ControlAddress(request.NodeID)
	if !ok {
		return runtimehost.Result{}, false, fmt.Errorf("resolve control endpoint for %q", request.NodeID)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return runtimehost.Result{}, false, fmt.Errorf("encode runtime lookup: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, "https://"+address+lookupPath, bytes.NewReader(body),
	)
	if err != nil {
		return runtimehost.Result{}, false, fmt.Errorf("build runtime lookup: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	client, err := c.clientFor(request.NodeID)
	if err != nil {
		return runtimehost.Result{}, false, err
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return runtimehost.Result{}, false, fmt.Errorf("lookup runtime result: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if err != nil || len(responseBody) > maxControlPayload {
		return runtimehost.Result{}, false, errors.New("invalid runtime lookup response")
	}
	if response.StatusCode != http.StatusOK {
		return runtimehost.Result{}, false, fmt.Errorf("runtime lookup rejected with status %d", response.StatusCode)
	}
	var lookup lookupResponse
	if err := json.Unmarshal(responseBody, &lookup); err != nil {
		return runtimehost.Result{}, false, errors.New("node returned a malformed runtime result")
	}
	if lookup.Failed {
		return lookup.Result, lookup.Found, errors.New("runtime operation failed")
	}
	return lookup.Result, lookup.Found, nil
}
