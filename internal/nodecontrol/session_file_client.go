package nodecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"
)

const sessionFilePath = "/v1/session/file"

func (c *Client) OpenSessionFile(
	ctx context.Context,
	query SessionFileQuery,
) (SessionFile, error) {
	address, ok := c.resolver.ControlAddress(query.NodeID)
	if !ok {
		return SessionFile{}, fmt.Errorf("resolve session file endpoint for %q", query.NodeID)
	}
	body, err := json.Marshal(query)
	if err != nil || len(body) > maxControlPayload {
		return SessionFile{}, errors.New("invalid session file query")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, "https://"+address+sessionFilePath, bytes.NewReader(body),
	)
	if err != nil {
		cancel()
		return SessionFile{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	client, err := c.clientFor(query.NodeID)
	if err != nil {
		cancel()
		return SessionFile{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		cancel()
		return SessionFile{}, fmt.Errorf("read remote session file: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxControlPayload))
		cancel()
		return SessionFile{}, fmt.Errorf("session file query rejected with status %d", response.StatusCode)
	}
	size, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	if err != nil || size < 0 || size > MaxSessionFileBytes {
		_ = response.Body.Close()
		cancel()
		return SessionFile{}, errors.New("node returned an invalid session file size")
	}
	_, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
	name := parameters["filename"]
	if err != nil || name == "" {
		_ = response.Body.Close()
		cancel()
		return SessionFile{}, errors.New("node returned invalid session file metadata")
	}
	return SessionFile{
		Name: name, MIMEType: response.Header.Get("Content-Type"), Size: size,
		Content: &cancelReadCloser{ReadCloser: response.Body, cancel: cancel},
	}, nil
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}
