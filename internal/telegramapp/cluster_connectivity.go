package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type ClusterConnectivityConfig struct {
	LocalNodeID domain.NodeID
	Interval    time.Duration
	LossGrace   time.Duration
}

type ClusterLeaderObserver interface {
	LeaderID() string
}

type connectivityEvent uint8

const (
	connectivityNoEvent connectivityEvent = iota
	connectivityLost
	connectivityRestored
)

type connectivityTracker struct {
	armed        bool
	lossStarted  time.Time
	lossNotified bool
}

func (t *connectivityTracker) observe(
	now time.Time,
	connected bool,
	enabled bool,
	grace time.Duration,
) connectivityEvent {
	if !enabled {
		*t = connectivityTracker{}
		return connectivityNoEvent
	}
	if connected {
		t.armed = true
		t.lossStarted = time.Time{}
		if t.lossNotified {
			return connectivityRestored
		}
		return connectivityNoEvent
	}
	if !t.armed || t.lossNotified {
		return connectivityNoEvent
	}
	if t.lossStarted.IsZero() {
		t.lossStarted = now
		return connectivityNoEvent
	}
	if now.Sub(t.lossStarted) >= grace {
		return connectivityLost
	}
	return connectivityNoEvent
}

func (t *connectivityTracker) acknowledge(event connectivityEvent) {
	switch event {
	case connectivityLost:
		t.lossNotified = true
	case connectivityRestored:
		t.lossNotified = false
	case connectivityNoEvent:
	}
}

// RunClusterConnectivityNotifications runs on every node, including followers.
// It only sends transport notifications and never mutates replicated state.
func (h *Handler) RunClusterConnectivityNotifications(
	ctx context.Context,
	leaders ClusterLeaderObserver,
	config ClusterConnectivityConfig,
) {
	if leaders == nil || config.LocalNodeID == "" {
		return
	}
	if config.Interval <= 0 {
		config.Interval = time.Second
	}
	if config.LossGrace <= 0 {
		config.LossGrace = 15 * time.Second
	}
	tracker := connectivityTracker{}
	ticker := time.NewTicker(config.Interval)
	defer ticker.Stop()
	for {
		h.observeClusterConnectivity(ctx, leaders, config, &tracker, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) observeClusterConnectivity(
	ctx context.Context,
	leaders ClusterLeaderObserver,
	config ClusterConnectivityConfig,
	tracker *connectivityTracker,
	now time.Time,
) {
	target, ok := h.service.ClusterAlertTarget(config.LocalNodeID)
	if !ok {
		return
	}
	event := tracker.observe(now, leaders.LeaderID() != "", target.Enabled, config.LossGrace)
	if event == connectivityNoEvent {
		return
	}
	kind := clusterEventNodeLost
	if event == connectivityRestored {
		kind = clusterEventNodeRestored
	}
	err := h.appendClusterEvent(ctx, target, clusterEvent{
		Kind: kind, NodeName: target.NodeName, At: now,
	})
	if err == nil {
		tracker.acknowledge(event)
	}
}
