package nodecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/sessiondescription"
)

const (
	archiveDescriptionPath       = "/v1/session/archive-description"
	maxArchiveDescriptionPayload = 8 << 10
)

func (c *Client) Generate(
	ctx context.Context,
	request sessiondescription.Request,
) (sessiondescription.Result, error) {
	address, ok := c.resolver.ControlAddress(string(request.NodeID))
	if !ok {
		return sessiondescription.Result{}, fmt.Errorf(
			"resolve archive description endpoint for %q", request.NodeID,
		)
	}
	body, err := json.Marshal(request)
	if err != nil || len(body) > maxControlPayload {
		return sessiondescription.Result{}, errors.New("invalid archive description request")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, "https://"+address+archiveDescriptionPath,
		bytes.NewReader(body),
	)
	if err != nil {
		return sessiondescription.Result{}, errors.New("build archive description request")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	client, err := c.clientFor(string(request.NodeID))
	if err != nil {
		return sessiondescription.Result{}, err
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return sessiondescription.Result{}, fmt.Errorf("generate remote archive description: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(
		response.Body, maxArchiveDescriptionPayload+1,
	))
	if err != nil || len(responseBody) > maxArchiveDescriptionPayload {
		return sessiondescription.Result{}, errors.New("invalid archive description response")
	}
	if response.StatusCode != http.StatusOK {
		return sessiondescription.Result{}, fmt.Errorf(
			"archive description rejected with status %d", response.StatusCode,
		)
	}
	var result sessiondescription.Result
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return sessiondescription.Result{}, errors.New("node returned malformed archive description")
	}
	return result, nil
}
