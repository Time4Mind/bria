// Package sessionstart coordinates node-local workspace discovery and provider
// startup without placing directory listings or transcripts in Raft.
package sessionstart

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
	"github.com/Time4Mind/bria/internal/workspace"
)

type BrowseRequest struct {
	ActorID domain.UserID `json:"actor_id"`
	NodeID  domain.NodeID `json:"node_id"`
	Path    string        `json:"path"`
}

type BrowseResult struct {
	Path        string                `json:"path"`
	Parent      string                `json:"parent,omitempty"`
	Directories []workspace.Directory `json:"directories"`
}

type DiscoverRequest struct {
	ActorID domain.UserID `json:"actor_id"`
	NodeID  domain.NodeID `json:"node_id"`
	Backend string        `json:"backend"`
	Workdir string        `json:"workdir"`
	Offset  int           `json:"offset,omitempty"`
	Limit   int           `json:"limit"`
	After   time.Time     `json:"after,omitempty"`
}

type ProvisionRequest struct {
	ActorID domain.UserID     `json:"actor_id"`
	Session domain.SessionRef `json:"session"`
}

type Service interface {
	Browse(context.Context, BrowseRequest) (BrowseResult, error)
	Discover(context.Context, DiscoverRequest) (transcript.Discovery, error)
	Provision(context.Context, ProvisionRequest) error
}
