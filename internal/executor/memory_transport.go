package executor

import (
	"context"
	"strings"
	"sync"

	"bria/internal/domain"
)

// MemoryTransport proves that local and remote routing use the same public
// boundary without exposing a network listener.
type MemoryTransport struct {
	mu     sync.RWMutex
	routes map[domain.ComputerID]Executor
}

var _ Transport = (*MemoryTransport)(nil)

func NewMemoryTransport() *MemoryTransport {
	return &MemoryTransport{routes: make(map[domain.ComputerID]Executor)}
}

func (transport *MemoryTransport) Register(id domain.ComputerID, route Executor) error {
	if transport == nil || strings.TrimSpace(string(id)) == "" || route == nil {
		return ErrInvalidRequest
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if _, exists := transport.routes[id]; exists {
		return ErrInvalidRequest
	}
	transport.routes[id] = route
	return nil
}

func (transport *MemoryTransport) Unregister(id domain.ComputerID) {
	if transport == nil {
		return
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	delete(transport.routes, id)
}

func (transport *MemoryTransport) Execute(ctx context.Context, target domain.ComputerID, request Request) (Response, error) {
	if transport == nil {
		return Response{}, ErrComputerOffline
	}
	transport.mu.RLock()
	route, exists := transport.routes[target]
	transport.mu.RUnlock()
	if !exists {
		return Response{}, ErrComputerOffline
	}
	return route.Execute(ctx, cloneRequest(request))
}
