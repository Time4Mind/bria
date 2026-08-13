package nodecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Time4Mind/bria/internal/clusterbackup"
)

type BackupLeaderError struct {
	Hint BackupLeaderHint
}

func (e *BackupLeaderError) Error() string {
	return fmt.Sprintf("cluster backup is served by leader %q", e.Hint.NodeID)
}

type BackupStatusError struct {
	StatusCode int
}

func (e *BackupStatusError) Error() string {
	return fmt.Sprintf("backup request rejected with status %d", e.StatusCode)
}

func (c *Client) Backup(ctx context.Context, nodeID string) (clusterbackup.Envelope, error) {
	address, ok := c.resolver.ControlAddress(nodeID)
	if !ok {
		return clusterbackup.Envelope{}, fmt.Errorf("resolve control endpoint for %q", nodeID)
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodGet, "https://"+address+backupPath, nil,
	)
	if err != nil {
		return clusterbackup.Envelope{}, fmt.Errorf("build backup request: %w", err)
	}
	client, err := c.clientFor(nodeID)
	if err != nil {
		return clusterbackup.Envelope{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return clusterbackup.Envelope{}, fmt.Errorf("read cluster backup: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, clusterbackup.MaxFileBytes+1))
	if err != nil || len(data) > clusterbackup.MaxFileBytes {
		return clusterbackup.Envelope{}, errors.New("invalid cluster backup response")
	}
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusConflict {
			var hint BackupLeaderHint
			if json.Unmarshal(data, &hint) == nil && hint.NodeID != "" &&
				hint.ControlAddress != "" {
				return clusterbackup.Envelope{}, &BackupLeaderError{Hint: hint}
			}
		}
		return clusterbackup.Envelope{}, &BackupStatusError{StatusCode: response.StatusCode}
	}
	return clusterbackup.Parse(data)
}
