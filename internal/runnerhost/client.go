package runnerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerbinding"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

type Client struct {
	http *http.Client
}

func NewClient(socket string) (*Client, error) {
	if strings.TrimSpace(socket) == "" {
		return nil, errors.New("runner socket is required")
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Client{http: &http.Client{Transport: transport, Timeout: 10 * time.Minute}}, nil
}

func (c *Client) Close() error {
	if transport, ok := c.http.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	return nil
}

func (c *Client) Lookup(ref domain.SessionRef, workdir string) (providerbinding.Record, bool, error) {
	return c.lookupBinding(bindingLookupRequest{Ref: ref, Workdir: workdir})
}

func (c *Client) LookupRef(ref domain.SessionRef) (providerbinding.Record, bool, error) {
	return c.lookupBinding(bindingLookupRequest{Ref: ref})
}

func (c *Client) lookupBinding(input bindingLookupRequest) (providerbinding.Record, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var response bindingLookupResponse
	if err := c.call(ctx, http.MethodPost, "/v1/provider-binding/lookup", input, &response); err != nil {
		return providerbinding.Record{}, false, err
	}
	if response.Error != "" {
		return providerbinding.Record{}, false, errors.New(response.Error)
	}
	return response.Record, response.Found, nil
}

func (c *Client) Snapshot() ([]providerbinding.Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var response bindingSnapshotResponse
	if err := c.call(ctx, http.MethodGet, "/v1/provider-binding/snapshot", nil, &response); err != nil {
		return nil, err
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	return response.Records, nil
}

func (c *Client) Sweep(input providerbinding.SweepInput) error {
	return c.bindingMutation("/v1/provider-binding/sweep", bindingSweepRequest{Input: input})
}

func (c *Client) DeleteIfGeneration(ref domain.SessionRef, generation uint64) error {
	return c.bindingMutation("/v1/provider-binding/delete", bindingDeleteRequest{Ref: ref, Generation: generation})
}

func (c *Client) bindingMutation(path string, input any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var response bindingMutationResponse
	if err := c.call(ctx, http.MethodPost, path, input, &response); err != nil {
		return err
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return nil
}

func (c *Client) Inspect(ctx context.Context) (Inspect, error) {
	var result Inspect
	if err := c.call(ctx, http.MethodGet, "/v1/inspect", nil, &result); err != nil {
		return Inspect{}, err
	}
	if result.ProtocolVersion != ProtocolVersion {
		return Inspect{}, fmt.Errorf("unsupported runner protocol %d", result.ProtocolVersion)
	}
	return result, nil
}

func (c *Client) LookPath(name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var response pathResponse
	if err := c.call(ctx, http.MethodPost, "/v1/look-path", pathRequest{Name: name}, &response); err != nil {
		return "", err
	}
	if response.Error != "" {
		return "", errors.New(response.Error)
	}
	return response.Path, nil
}

func (c *Client) Run(ctx context.Context, name string, args ...string) (runtimehost.CommandResult, error) {
	return c.run(ctx, commandRequest{Kind: "run", Name: name, Args: args})
}

func (c *Client) RunInput(ctx context.Context, input []byte, name string, args ...string) (runtimehost.CommandResult, error) {
	return c.run(ctx, commandRequest{Kind: "input", Name: name, Args: args, Input: input})
}

func (c *Client) RunJSONRPC(
	ctx context.Context,
	initialize []byte,
	requests []byte,
	expectedID int,
	name string,
	args ...string,
) (runtimehost.CommandResult, error) {
	return c.run(ctx, commandRequest{
		Kind: "jsonrpc", Name: name, Args: args, Input: requests,
		Initialize: initialize, ExpectedID: expectedID,
	})
}

func (c *Client) run(ctx context.Context, request commandRequest) (runtimehost.CommandResult, error) {
	request.TimeoutMS = timeoutMilliseconds(ctx)
	var response commandResponse
	if err := c.call(ctx, http.MethodPost, "/v1/run", request, &response); err != nil {
		return runtimehost.CommandResult{}, err
	}
	if response.Error != "" {
		return response.Result, errors.New(response.Error)
	}
	return response.Result, nil
}

func (c *Client) call(ctx context.Context, method, path string, input, output any) error {
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://runner"+path, &body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("call runner: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("runner returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(output); err != nil {
		return fmt.Errorf("decode runner response: %w", err)
	}
	return nil
}

func timeoutMilliseconds(ctx context.Context) int64 {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < time.Millisecond {
			return 1
		}
		if remaining > 10*time.Minute {
			remaining = 10 * time.Minute
		}
		return int64(remaining / time.Millisecond)
	}
	return int64((10 * time.Minute) / time.Millisecond)
}

var _ runtimehost.JSONRPCCommandRunner = (*Client)(nil)
