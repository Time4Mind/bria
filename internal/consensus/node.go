// Package consensus wraps HashiCorp Raft behind Bria-specific contracts.
package consensus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

var ErrOutcomeUnknown = errors.New("consensus outcome is unknown")

type Config struct {
	NodeID         string
	DataDir        string
	Bootstrap      bool
	ApplyTimeout   time.Duration
	SnapshotRetain int
	LogOutput      io.Writer
	RaftConfig     *raft.Config
}

type Node struct {
	raft         *raft.Raft
	fsm          *clusterstate.FSM
	transport    raft.Transport
	closers      []io.Closer
	applyWait    time.Duration
	membershipMu sync.Mutex
	closeOnce    sync.Once
	closeErr     error
}

func Open(config Config, fsm *clusterstate.FSM, transport raft.Transport) (*Node, error) {
	if fsm == nil {
		return nil, errors.New("state machine is required")
	}
	if transport == nil {
		return nil, errors.New("raft transport is required")
	}
	if config.NodeID == "" {
		return nil, errors.New("node id is required")
	}
	if config.DataDir == "" {
		return nil, errors.New("raft data directory is required")
	}
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create raft data directory: %w", err)
	}
	if err := os.Chmod(config.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure raft data directory: %w", err)
	}

	boltPath := filepath.Join(config.DataDir, "raft.db")
	boltStore, err := raftboltdb.NewBoltStore(boltPath)
	if err != nil {
		return nil, fmt.Errorf("open raft bolt store: %w", err)
	}
	if err := os.Chmod(boltPath, 0o600); err != nil {
		_ = boltStore.Close()
		return nil, fmt.Errorf("secure raft bolt store: %w", err)
	}
	retain := config.SnapshotRetain
	if retain <= 0 {
		retain = 3
	}
	snapshots, err := raft.NewFileSnapshotStoreWithLogger(
		config.DataDir,
		retain,
		newRaftLogger(config.LogOutput),
	)
	if err != nil {
		_ = boltStore.Close()
		return nil, fmt.Errorf("open raft snapshot store: %w", err)
	}
	return newNode(
		config,
		fsm,
		boltStore,
		boltStore,
		snapshots,
		transport,
		[]io.Closer{boltStore},
	)
}

func NewInMemory(config Config, fsm *clusterstate.FSM) (*Node, error) {
	_, transport := raft.NewInmemTransport(raft.ServerAddress(config.NodeID))
	config.Bootstrap = true
	return NewInMemoryWithTransport(config, fsm, transport)
}

// NewInMemoryWithTransport creates an in-memory node on a caller-provided
// transport. Unlike NewInMemory, it preserves Config.Bootstrap so callers can
// assemble multi-node test clusters with exactly one bootstrap member.
func NewInMemoryWithTransport(
	config Config,
	fsm *clusterstate.FSM,
	transport raft.Transport,
) (*Node, error) {
	if fsm == nil {
		return nil, errors.New("state machine is required")
	}
	if transport == nil {
		return nil, errors.New("raft transport is required")
	}
	if config.NodeID == "" {
		return nil, errors.New("node id is required")
	}
	store := raft.NewInmemStore()
	return newNode(
		config,
		fsm,
		store,
		store,
		raft.NewInmemSnapshotStore(),
		transport,
		nil,
	)
}

func newNode(
	config Config,
	fsm *clusterstate.FSM,
	logStore raft.LogStore,
	stableStore raft.StableStore,
	snapshotStore raft.SnapshotStore,
	transport raft.Transport,
	closers []io.Closer,
) (*Node, error) {
	raftConfig := config.RaftConfig
	if raftConfig == nil {
		raftConfig = raft.DefaultConfig()
	} else {
		copyConfig := *raftConfig
		raftConfig = &copyConfig
	}
	raftConfig.LocalID = raft.ServerID(config.NodeID)
	raftConfig.Logger = newRaftLogger(config.LogOutput)

	instance, err := raft.NewRaft(
		raftConfig,
		fsm,
		logStore,
		stableStore,
		snapshotStore,
		transport,
	)
	if err != nil {
		closeAll(closers)
		return nil, fmt.Errorf("create raft node: %w", err)
	}
	node := &Node{
		raft:      instance,
		fsm:       fsm,
		transport: transport,
		closers:   closers,
		applyWait: config.ApplyTimeout,
	}
	if node.applyWait <= 0 {
		node.applyWait = 10 * time.Second
	}

	existing, err := raft.HasExistingState(logStore, stableStore, snapshotStore)
	if err != nil {
		_ = node.Close()
		return nil, fmt.Errorf("inspect raft state: %w", err)
	}
	if config.Bootstrap && !existing {
		future := instance.BootstrapCluster(raft.Configuration{Servers: []raft.Server{{
			ID:       raft.ServerID(config.NodeID),
			Address:  transport.LocalAddr(),
			Suffrage: raft.Voter,
		}}})
		if err := future.Error(); err != nil && !errors.Is(err, raft.ErrCantBootstrap) {
			_ = node.Close()
			return nil, fmt.Errorf("bootstrap raft cluster: %w", err)
		}
	}
	return node, nil
}

func (n *Node) Apply(ctx context.Context, command clusterstate.Command) (clusterstate.Result, error) {
	data, err := json.Marshal(command)
	if err != nil {
		return clusterstate.Result{}, fmt.Errorf("encode consensus command: %w", err)
	}
	timeout := n.applyWait
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return clusterstate.Result{}, context.DeadlineExceeded
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	future := n.raft.Apply(data, timeout)
	done := make(chan error, 1)
	go func() { done <- future.Error() }()
	select {
	case err := <-done:
		if err != nil {
			return clusterstate.Result{}, err
		}
		result, ok := future.Response().(clusterstate.Result)
		if !ok {
			return clusterstate.Result{}, fmt.Errorf("unexpected state machine response: %T", future.Response())
		}
		return result, nil
	case <-ctx.Done():
		return clusterstate.Result{}, fmt.Errorf("%w: operation %s: %v", ErrOutcomeUnknown, command.OperationID, ctx.Err())
	}
}

func (n *Node) State() *clusterstate.Machine {
	return n.fsm.Machine()
}

func (n *Node) IsLeader() bool {
	return n.raft.State() == raft.Leader
}

// LeaderID returns the stable authenticated identity of the currently known
// leader. An empty value means Raft has not established a leader yet.
func (n *Node) LeaderID() string {
	_, id := n.raft.LeaderWithID()
	return string(id)
}

// LeadershipChanges reports transitions into and out of local Raft
// leadership. Hashicorp Raft may coalesce transitions for slow consumers, so
// handlers should use the signal as a wake-up and confirm current state with
// IsLeader before starting leader-only work.
func (n *Node) LeadershipChanges() <-chan bool {
	return n.raft.LeaderCh()
}

func (n *Node) WaitForLeader(ctx context.Context) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, id := n.raft.LeaderWithID(); id != "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (n *Node) AddVoter(id, address string, timeout time.Duration) error {
	return n.raft.AddVoter(raft.ServerID(id), raft.ServerAddress(address), 0, timeout).Error()
}

func (n *Node) RemoveServer(id string, timeout time.Duration) error {
	return n.raft.RemoveServer(raft.ServerID(id), 0, timeout).Error()
}

func (n *Node) Stats() map[string]string {
	return n.raft.Stats()
}

func (n *Node) Close() error {
	n.closeOnce.Do(func() {
		var errs []error
		if err := n.raft.Shutdown().Error(); err != nil {
			errs = append(errs, err)
		}
		if closer, ok := n.transport.(raft.WithClose); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		for _, closer := range n.closers {
			if err := closer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		n.closeErr = errors.Join(errs...)
	})
	return n.closeErr
}

func closeAll(closers []io.Closer) {
	for _, closer := range closers {
		_ = closer.Close()
	}
}
