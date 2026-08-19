package nodecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/clusterstate"
)

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
