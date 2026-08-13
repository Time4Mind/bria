package clusterupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

type StateReader interface{ State() *domain.State }

type Consensus interface {
	Apply(context.Context, clusterstate.Command) (clusterstate.Result, error)
	IsLeader() bool
	LeaderID() string
	TransferLeadershipTo(string) error
}

type Coordinator struct {
	localNodeID domain.NodeID
	reader      StateReader
	consensus   Consensus
	nodes       Service
	now         func() time.Time
	timeout     time.Duration
}

func NewCoordinator(
	localNodeID string, reader StateReader, consensus Consensus, nodes Service,
) (*Coordinator, error) {
	if localNodeID == "" || reader == nil || consensus == nil || nodes == nil {
		return nil, errors.New("cluster update coordinator dependencies are required")
	}
	return &Coordinator{
		localNodeID: domain.NodeID(localNodeID), reader: reader,
		consensus: consensus, nodes: nodes, now: time.Now, timeout: 15 * time.Minute,
	}, nil
}

func (c *Coordinator) Start(ctx context.Context) (domain.ClusterUpdate, error) {
	if !c.consensus.IsLeader() {
		return domain.ClusterUpdate{}, errors.New("cluster update must start on the leader")
	}
	manifest, err := c.nodes.Inspect(ctx)
	if err != nil {
		return domain.ClusterUpdate{}, err
	}
	state := c.reader.State()
	if state.ClusterUpdate != nil && state.ClusterUpdate.Active() {
		return domain.ClusterUpdate{}, errors.New("cluster update already running")
	}
	order, err := updateOrder(state, domain.NodeID(c.consensus.LeaderID()), manifest.Manifest)
	if err != nil {
		return domain.ClusterUpdate{}, err
	}
	current := true
	for _, nodeID := range order {
		if state.Nodes[nodeID].Version != manifest.Version {
			current = false
			break
		}
	}
	if current {
		return domain.ClusterUpdate{}, fmt.Errorf("cluster already runs %s", manifest.Version)
	}
	for _, nodeID := range order {
		if _, err := c.nodes.Status(ctx, Request{NodeID: string(nodeID)}); err != nil {
			return domain.ClusterUpdate{}, fmt.Errorf(
				"node %s cannot update: %w", state.Nodes[nodeID].Name, err,
			)
		}
	}
	id, err := updateOperationID()
	if err != nil {
		return domain.ClusterUpdate{}, err
	}
	update := domain.ClusterUpdate{
		ID: id, Version: manifest.Version, ManifestSHA256: manifest.SHA256, Order: order,
	}
	if err := c.apply(ctx, clusterstate.CommandBeginClusterUpdate, update); err != nil {
		return domain.ClusterUpdate{}, err
	}
	return *c.reader.State().ClusterUpdate, nil
}

func updateOrder(
	state *domain.State, leaderID domain.NodeID, manifest Manifest,
) ([]domain.NodeID, error) {
	nodes := make([]domain.Node, 0, len(state.Nodes))
	for _, node := range state.Nodes {
		if !node.Enabled() {
			continue
		}
		if node.Status != domain.NodeOnline {
			return nil, fmt.Errorf("node %s is offline", node.Name)
		}
		if _, ok := manifest.CompatibleArtifact(node.OS, node.Arch); !ok {
			return nil, fmt.Errorf("release does not support %s (%s/%s)", node.Name, node.OS, node.Arch)
		}
		nodes = append(nodes, node)
	}
	if len(nodes) == 0 {
		return nil, errors.New("cluster has no enabled nodes")
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].ID == leaderID {
			return false
		}
		if nodes[j].ID == leaderID {
			return true
		}
		if !nodes[i].CreatedAt.Equal(nodes[j].CreatedAt) {
			return nodes[i].CreatedAt.Before(nodes[j].CreatedAt)
		}
		return nodes[i].ID < nodes[j].ID
	})
	order := make([]domain.NodeID, len(nodes))
	for index, node := range nodes {
		order[index] = node.ID
	}
	return order, nil
}

func (c *Coordinator) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("cluster update interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if c.consensus.IsLeader() {
				c.reconcile(ctx)
			}
		}
	}
}

func (c *Coordinator) reconcile(ctx context.Context) {
	state := c.reader.State()
	if state.ClusterUpdate == nil || !state.ClusterUpdate.Active() {
		return
	}
	update := *state.ClusterUpdate
	for _, nodeID := range update.Order {
		nodeUpdate := update.Nodes[nodeID]
		if nodeUpdate.Phase == domain.NodeUpdateHealthy {
			continue
		}
		node := state.Nodes[nodeID]
		if node.Status == domain.NodeOnline && node.Version == update.Version {
			_ = c.setNode(ctx, update.ID, nodeID, domain.NodeUpdateHealthy, "")
			return
		}
		switch nodeUpdate.Phase {
		case domain.NodeUpdatePending:
			c.startNext(ctx, update, nodeID)
		case domain.NodeUpdateInstalling:
			c.observeInstalling(ctx, update, nodeID, nodeUpdate)
		case domain.NodeUpdateFailed:
			_ = c.finish(ctx, update.ID, true, nodeUpdate.Error)
		}
		return
	}
	_ = c.finish(ctx, update.ID, false, "")
}

func (c *Coordinator) startNext(
	ctx context.Context, update domain.ClusterUpdate, nodeID domain.NodeID,
) {
	leaderID := domain.NodeID(c.consensus.LeaderID())
	if nodeID == leaderID && len(update.Order) > 1 {
		for _, candidate := range update.Order {
			node := c.reader.State().Nodes[candidate]
			if candidate != leaderID && node.Status == domain.NodeOnline &&
				node.Version == update.Version {
				_ = c.consensus.TransferLeadershipTo(string(candidate))
				return
			}
		}
		c.failNode(ctx, update.ID, nodeID, errors.New("no updated node can accept leadership"))
		return
	}
	if err := c.setNode(ctx, update.ID, nodeID, domain.NodeUpdateInstalling, ""); err != nil {
		return
	}
	_, err := c.nodes.Start(ctx, Request{
		NodeID: string(nodeID), UpdateID: update.ID,
		Version: update.Version, ManifestSHA256: update.ManifestSHA256,
	})
	if err != nil {
		c.failNode(ctx, update.ID, nodeID, err)
	}
}

func (c *Coordinator) observeInstalling(
	ctx context.Context, update domain.ClusterUpdate, nodeID domain.NodeID, nodeUpdate domain.NodeUpdate,
) {
	if c.now().Sub(nodeUpdate.UpdatedAt) > c.timeout {
		c.failNode(ctx, update.ID, nodeID, errors.New("node update timed out"))
		return
	}
	status, err := c.nodes.Status(ctx, Request{NodeID: string(nodeID), UpdateID: update.ID})
	if err == nil && status.Phase == PhaseFailed {
		c.failNode(ctx, update.ID, nodeID, errors.New(status.Error))
	}
}

func (c *Coordinator) failNode(ctx context.Context, updateID string, nodeID domain.NodeID, err error) {
	detail := strings.TrimSpace(err.Error())
	if c.setNode(ctx, updateID, nodeID, domain.NodeUpdateFailed, detail) == nil {
		_ = c.finish(ctx, updateID, true, detail)
	}
}

func (c *Coordinator) setNode(
	ctx context.Context, updateID string, nodeID domain.NodeID,
	phase domain.NodeUpdatePhase, detail string,
) error {
	return c.apply(ctx, clusterstate.CommandSetClusterUpdateNode, clusterstate.SetClusterUpdateNode{
		UpdateID: updateID, NodeID: nodeID, Phase: phase, Error: detail,
	})
}

func (c *Coordinator) finish(ctx context.Context, updateID string, failed bool, detail string) error {
	return c.apply(ctx, clusterstate.CommandFinishClusterUpdate, clusterstate.FinishClusterUpdate{
		UpdateID: updateID, Failed: failed, Error: detail,
	})
}

func (c *Coordinator) apply(ctx context.Context, kind clusterstate.CommandKind, payload any) error {
	id, err := updateOperationID()
	if err != nil {
		return err
	}
	command, err := clusterstate.NewCommand(id, kind, c.now(), payload)
	if err != nil {
		return err
	}
	result, err := c.consensus.Apply(ctx, command)
	if err != nil {
		return err
	}
	return result.Err()
}

func updateOperationID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return "update-" + hex.EncodeToString(buffer), nil
}
