package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
)

func connectedLocalBackends(
	state *domain.State, nodeID domain.NodeID, installed []domain.BackendDescriptor,
) []domain.BackendDescriptor {
	if state == nil {
		return nil
	}
	node, ok := state.Nodes[nodeID]
	if !ok || !node.BackendSelectionInitialized {
		return nil
	}
	connected := make(map[string]bool, len(node.Backends))
	for _, backend := range node.Backends {
		connected[strings.ToLower(backend.Name)] = true
	}
	result := make([]domain.BackendDescriptor, 0, len(installed))
	for _, backend := range installed {
		if connected[strings.ToLower(backend.Name)] {
			result = append(result, backend)
		}
	}
	return result
}

func newOperationID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate operation id: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}
