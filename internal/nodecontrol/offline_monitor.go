package nodecontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

type LocalLeadership interface {
	IsLeader() bool
}

type OfflineMonitor struct {
	leader   LocalLeadership
	state    StateReader
	apply    CommandApplier
	timeout  time.Duration
	interval time.Duration
	now      func() time.Time
}

func NewOfflineMonitor(
	leader LocalLeadership,
	state StateReader,
	apply CommandApplier,
	timeout time.Duration,
	interval time.Duration,
) (*OfflineMonitor, error) {
	if leader == nil || state == nil || apply == nil {
		return nil, errors.New("offline monitor dependencies are required")
	}
	if timeout <= 0 || interval <= 0 || interval >= timeout {
		return nil, errors.New("offline monitor requires 0 < interval < timeout")
	}
	return &OfflineMonitor{
		leader: leader, state: state, apply: apply, timeout: timeout,
		interval: interval, now: time.Now,
	}, nil
}

func (m *OfflineMonitor) Sweep(ctx context.Context) error {
	if !m.leader.IsLeader() {
		return nil
	}
	now := m.now().UTC()
	state := m.state.State()
	if state == nil {
		return errors.New("cluster state is unavailable")
	}
	var failures []error
	for nodeID, node := range state.Nodes {
		if node.Status == domain.NodeOffline || node.LastSeenAt.IsZero() ||
			now.Sub(node.LastSeenAt) <= m.timeout {
			continue
		}
		command, err := clusterstate.NewCommand(
			offlineOperationID(nodeID, node.LastSeenAt),
			clusterstate.CommandMarkNodeOffline,
			now,
			clusterstate.MarkNodeOffline{
				NodeID: nodeID, ObservedLastSeenAt: node.LastSeenAt,
			},
		)
		if err == nil {
			var result clusterstate.Result
			result, err = m.apply.Apply(ctx, command)
			if err == nil {
				err = result.Err()
			}
		}
		if err != nil && err.Error() != domain.ErrStaleOperation.Error() {
			failures = append(failures, fmt.Errorf("mark node %s offline: %w", nodeID, err))
		}
	}
	return errors.Join(failures...)
}

func (m *OfflineMonitor) Run(ctx context.Context, errorsOut chan<- error) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.Sweep(ctx); err != nil && errorsOut != nil {
				select {
				case errorsOut <- err:
				default:
				}
			}
		}
	}
}

func offlineOperationID(nodeID domain.NodeID, seenAt time.Time) string {
	digest := sha256.Sum256([]byte(string(nodeID) + "\x00" + seenAt.UTC().Format(time.RFC3339Nano)))
	return "node-offline-" + hex.EncodeToString(digest[:16])
}
