package enrollment

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Time4Mind/bria/internal/security"
)

type Client struct {
	endpoint string
	http     *http.Client
}

func NewClient(invitation security.ClusterInvitation, timeout time.Duration) (*Client, error) {
	return newClient(invitation.Endpoint, invitation.ClusterID, invitation.IssuerNodeID,
		invitation.CACertificate, timeout)
}

func NewClaimClient(claim security.EnrollmentClaim, timeout time.Duration) (*Client, error) {
	return newClient(claim.Endpoint, claim.ClusterID, claim.IssuerNodeID,
		claim.CACertificate, timeout)
}

func newClient(endpoint, clusterID, issuerNodeID, caCertificate string, timeout time.Duration) (*Client, error) {
	roots, err := security.CertificatePool([]byte(caCertificate))
	if err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true}
	tlsConfig.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("enrollment server sent no certificate")
		}
		return security.VerifyNodeCertificate(state.PeerCertificates[0], roots,
			clusterID, issuerNodeID, time.Now(), x509.ExtKeyUsageServerAuth)
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, TLSHandshakeTimeout: timeout}
	return &Client{endpoint: "https://" + endpoint,
		http: &http.Client{Transport: transport, Timeout: timeout}}, nil
}

func (c *Client) Register(
	ctx context.Context,
	invitation security.ClusterInvitation,
	contract string,
) (RegisterResponse, error) {
	var response RegisterResponse
	err := c.post(ctx, registerPath, RegisterRequest{
		TokenID: invitation.TokenID, Secret: invitation.Secret, Contract: contract,
	}, http.StatusAccepted, &response)
	return response, err
}

func (c *Client) Status(
	ctx context.Context,
	requestID string,
	privateKey ed25519.PrivateKey,
) (StatusResponse, error) {
	proof, err := NewStatusRequest(requestID, privateKey, time.Now())
	if err != nil {
		return StatusResponse{}, err
	}
	var response StatusResponse
	err = c.post(ctx, statusPath, proof, http.StatusOK, &response)
	return response, err
}

func (c *Client) post(ctx context.Context, path string, input any, want int, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPayload+1))
	if err != nil || len(data) > maxPayload {
		return errors.New("invalid enrollment response")
	}
	if response.StatusCode != want {
		return fmt.Errorf("enrollment request rejected with status %d", response.StatusCode)
	}
	if err := json.Unmarshal(data, output); err != nil {
		return errors.New("malformed enrollment response")
	}
	return nil
}
