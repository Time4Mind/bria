package nodecontrol

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type HeartbeatPublisher interface {
	PublishHeartbeat(context.Context, string, Heartbeat) (HeartbeatAck, error)
}

type HeartbeatSnapshot func(context.Context) (Heartbeat, error)

// HeartbeatAgent follows Raft's current leader instead of pinning reports to a
// configured bootstrap node. Run owns no global goroutines and stops with ctx.
type HeartbeatAgent struct {
	leaders  Leadership
	publish  HeartbeatPublisher
	snapshot HeartbeatSnapshot
	interval time.Duration
	newID    func() (string, error)
}

func NewHeartbeatAgent(
	leaders Leadership,
	publish HeartbeatPublisher,
	snapshot HeartbeatSnapshot,
	interval time.Duration,
) (*HeartbeatAgent, error) {
	if leaders == nil || publish == nil || snapshot == nil {
		return nil, errors.New("heartbeat leadership, publisher, and snapshot are required")
	}
	if interval <= 0 {
		return nil, errors.New("heartbeat interval must be positive")
	}
	return &HeartbeatAgent{
		leaders: leaders, publish: publish, snapshot: snapshot,
		interval: interval, newID: heartbeatReportID,
	}, nil
}

func (a *HeartbeatAgent) PublishOnce(ctx context.Context) (HeartbeatAck, error) {
	leaderID := a.leaders.LeaderID()
	if leaderID == "" {
		return HeartbeatAck{}, errors.New("Raft leader is not known")
	}
	report, err := a.snapshot(ctx)
	if err != nil {
		return HeartbeatAck{}, fmt.Errorf("collect heartbeat: %w", err)
	}
	report.ReportID, err = a.newID()
	if err != nil {
		return HeartbeatAck{}, fmt.Errorf("create heartbeat report id: %w", err)
	}
	return a.publish.PublishHeartbeat(ctx, leaderID, report)
}

func (a *HeartbeatAgent) Run(
	ctx context.Context,
	errorsOut chan<- error,
	recoveryOut chan<- domain.BootRecoveryPlan,
) {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		ack, err := a.PublishOnce(ctx)
		if err != nil && ctx.Err() == nil && errorsOut != nil {
			select {
			case errorsOut <- err:
			default:
			}
		} else if err == nil && recoveryOut != nil && len(ack.Recovery.Recover) > 0 {
			select {
			case recoveryOut <- ack.Recovery:
			default:
				if errorsOut != nil {
					select {
					case errorsOut <- errors.New("boot recovery queue is busy"):
					default:
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func heartbeatReportID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
