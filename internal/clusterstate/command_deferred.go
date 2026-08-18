package clusterstate

import "github.com/Time4Mind/bria/internal/domain"

type QueueDeferredInput struct {
	Input domain.DeferredSessionInput `json:"input"`
}

type ResolveDeferredInput struct {
	Session     domain.SessionRef `json:"session"`
	OperationID string            `json:"operation_id"`
	Failed      bool              `json:"failed,omitempty"`
	Detail      string            `json:"detail,omitempty"`
}
