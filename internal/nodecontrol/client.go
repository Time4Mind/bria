package nodecontrol

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

const (
	executePath         = "/v1/session/commands"
	lookupPath          = "/v1/session/results"
	membershipAdminPath = "/v1/cluster/membership"
	membershipMovePath  = "/v1/cluster/membership/relocate"
	maxControlPayload   = 64 << 10
)

type ClientConfig struct {
	Certificate tls.Certificate
	Roots       *x509.CertPool
	ClusterID   string
	Resolver    Resolver
	Timeout     time.Duration
}

type MembershipRelocation struct {
	NodeID            string `json:"node_id"`
	RaftAddress       string `json:"raft_address"`
	ControlAddress    string `json:"control_address"`
	EnrollmentAddress string `json:"enrollment_address,omitempty"`
}

func (c *Client) RelocateMembership(
	ctx context.Context,
	leaderID string,
	relocation MembershipRelocation,
) error {
	return c.postMembership(ctx, leaderID, membershipMovePath, relocation)
}

func (c *Client) ApplyMembershipCommand(
	ctx context.Context,
	nodeID string,
	command clusterstate.Command,
) error {
	return c.postMembership(ctx, nodeID, membershipAdminPath, command)
}

func (c *Client) postMembership(ctx context.Context, nodeID, path string, payload any) error {
	address, ok := c.resolver.ControlAddress(nodeID)
	if !ok {
		return fmt.Errorf("resolve control endpoint for %q", nodeID)
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > maxControlPayload {
		return errors.New("invalid membership command")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, "https://"+address+path, bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client, err := c.clientFor(nodeID)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, readErr := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if readErr != nil || (response.StatusCode != http.StatusOK &&
		response.StatusCode != http.StatusNoContent) {
		return fmt.Errorf("membership command rejected with status %d", response.StatusCode)
	}
	return nil
}

type lookupResponse struct {
	Found  bool               `json:"found"`
	Failed bool               `json:"failed,omitempty"`
	Result runtimehost.Result `json:"result"`
}

type Client struct {
	certificate tls.Certificate
	roots       *x509.CertPool
	clusterID   string
	resolver    Resolver
	timeout     time.Duration

	mu      sync.Mutex
	clients map[string]cachedHTTPClient
}

type cachedHTTPClient struct {
	client      *http.Client
	fingerprint string
}

func NewClient(config ClientConfig) (*Client, error) {
	if len(config.Certificate.Certificate) == 0 || config.Roots == nil ||
		config.ClusterID == "" || config.Resolver == nil {
		return nil, errors.New("node-control client identity and resolver are required")
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Client{
		certificate: config.Certificate, roots: config.Roots, clusterID: config.ClusterID,
		resolver: config.Resolver, timeout: timeout, clients: make(map[string]cachedHTTPClient),
	}, nil
}

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
	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK {
		return runtimehost.Receipt{}, fmt.Errorf("runtime request rejected with status %d", response.StatusCode)
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

// Probe checks the authenticated process or quorum readiness of a cluster node.
func (c *Client) Probe(
	ctx context.Context,
	nodeID string,
	readiness bool,
) (HealthStatus, error) {
	address, ok := c.resolver.ControlAddress(nodeID)
	if !ok {
		return HealthStatus{}, fmt.Errorf("resolve control endpoint for %q", nodeID)
	}
	path := healthPath
	if readiness {
		path = readinessPath
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodGet, "https://"+address+path, nil,
	)
	if err != nil {
		return HealthStatus{}, fmt.Errorf("build health request: %w", err)
	}
	client, err := c.clientFor(nodeID)
	if err != nil {
		return HealthStatus{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return HealthStatus{}, fmt.Errorf("probe node: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if err != nil || len(body) > maxControlPayload {
		return HealthStatus{}, errors.New("invalid health response")
	}
	var status HealthStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return HealthStatus{}, errors.New("node returned malformed health status")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusServiceUnavailable {
		return HealthStatus{}, fmt.Errorf("health probe rejected with status %d", response.StatusCode)
	}
	return status, nil
}

func (c *Client) Metrics(ctx context.Context, nodeID string) (string, error) {
	address, ok := c.resolver.ControlAddress(nodeID)
	if !ok {
		return "", fmt.Errorf("resolve control endpoint for %q", nodeID)
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodGet, "https://"+address+metricsPath, nil,
	)
	if err != nil {
		return "", fmt.Errorf("build metrics request: %w", err)
	}
	client, err := c.clientFor(nodeID)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("read node metrics: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxControlPayload+1))
	if err != nil || len(body) > maxControlPayload {
		return "", errors.New("invalid metrics response")
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metrics request rejected with status %d", response.StatusCode)
	}
	return string(body), nil
}

func (c *Client) clientFor(nodeID string) (*http.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fingerprint := ""
	if resolver, ok := c.resolver.(CertificateResolver); ok {
		fingerprint, _ = resolver.NodeFingerprint(nodeID)
	}
	if cached, ok := c.clients[nodeID]; ok && cached.fingerprint == fingerprint {
		return cached.client, nil
	} else if ok {
		cached.client.CloseIdleConnections()
	}
	tlsConfig, err := clientTLSConfig(
		c.certificate, c.roots, c.clusterID, nodeID, fingerprint,
	)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout: c.timeout, KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   c.timeout,
		ResponseHeaderTimeout: c.timeout,
		MaxIdleConnsPerHost:   2,
	}
	client := &http.Client{Transport: transport}
	c.clients[nodeID] = cachedHTTPClient{client: client, fingerprint: fingerprint}
	return client, nil
}

func (c *Client) CloseIdleConnections() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cached := range c.clients {
		cached.client.CloseIdleConnections()
	}
}
