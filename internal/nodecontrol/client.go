package nodecontrol

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

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
