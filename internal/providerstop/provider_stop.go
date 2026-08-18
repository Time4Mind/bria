// Package providerstop carries provider turn-completion hints to the current
// interaction leader. A hint is never treated as proof of completion: the
// consumer must still verify the canonical provider transcript.
package providerstop

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const defaultBuffer = 256

var ErrLeaderUnavailable = errors.New("provider stop leader unavailable")

type Signal struct {
	NodeID            string `json:"node_id"`
	SessionID         string `json:"session_id"`
	ProviderSessionID string `json:"provider_session_id"`
}

func (s Signal) Validate() error {
	for label, value := range map[string]string{
		"node id": s.NodeID, "session id": s.SessionID,
		"provider session id": s.ProviderSessionID,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 256 ||
			strings.ContainsAny(value, "\x00\r\n\t") {
			return fmt.Errorf("invalid provider stop %s", label)
		}
	}
	return nil
}

type Service interface {
	Notify(context.Context, Signal) error
}

type Remote interface {
	NotifyProviderStop(context.Context, string, Signal) error
}

type Leadership interface {
	LeaderID() string
}

// Bus retains a small burst across adapter startup and provides backpressure
// instead of silently dropping completion hints.
type Bus struct {
	events chan Signal
}

func NewBus(buffer int) *Bus {
	if buffer <= 0 {
		buffer = defaultBuffer
	}
	return &Bus{events: make(chan Signal, buffer)}
}

func (b *Bus) Notify(ctx context.Context, signal Signal) error {
	if b == nil {
		return errors.New("provider stop bus is unavailable")
	}
	if err := signal.Validate(); err != nil {
		return err
	}
	select {
	case b.events <- signal:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Bus) Events() <-chan Signal {
	if b == nil {
		return nil
	}
	return b.events
}

// Router re-evaluates leadership at every hop. A hook always calls its local
// node, while a follower forwards the authenticated hint to the current leader.
type Router struct {
	localNodeID string
	leadership  Leadership
	local       Service
	remote      Remote
}

func NewRouter(
	localNodeID string,
	leadership Leadership,
	local Service,
	remote Remote,
) (*Router, error) {
	if strings.TrimSpace(localNodeID) == "" || leadership == nil || local == nil || remote == nil {
		return nil, errors.New("provider stop router dependencies are required")
	}
	return &Router{
		localNodeID: localNodeID, leadership: leadership, local: local, remote: remote,
	}, nil
}

func (r *Router) Notify(ctx context.Context, signal Signal) error {
	if err := signal.Validate(); err != nil {
		return err
	}
	leaderID := r.leadership.LeaderID()
	if leaderID == "" {
		return ErrLeaderUnavailable
	}
	if leaderID == r.localNodeID {
		return r.local.Notify(ctx, signal)
	}
	return r.remote.NotifyProviderStop(ctx, leaderID, signal)
}
