package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/platform"
	"github.com/Time4Mind/bria/internal/quota"
	"github.com/Time4Mind/bria/internal/recovery"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

const (
	nodeHeartbeatInterval = 2 * time.Second
	nodeOfflineTimeout    = 8 * time.Second
	nodeOfflineSweep      = time.Second
	nodeInventoryRefresh  = 30 * time.Second
)

func startNodeHeartbeatLoops(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
	client *nodecontrol.Client,
	localExecutor *runtimehost.LocalExecutor,
	archiveVerifier archiveVerifier,
	transcripts transcriptEventReader,
) error {
	bootID, err := platform.NewBootIDProvider().Current(ctx)
	if err != nil {
		return fmt.Errorf("read host boot id for heartbeat: %w", err)
	}
	inventory := newNodeInventory(discoverLocalBackends(ctx))
	runner := runtimehost.ExecCommandRunner{}
	claudeQuota, err := quota.NewClaudeCollector(
		runner, nodeConfig.TmuxSession, nodeConfig.ClaudeCommand, nodeConfig.EffectiveClaudeFlags(),
	)
	if err != nil {
		return err
	}
	codexQuota, err := quota.NewCodexCollector(runner, nodeConfig.CodexCommand, nodeConfig.EffectiveCodexFlags())
	if err != nil {
		return err
	}
	quotaStore := quota.NewStore(domain.NodeID(nodeConfig.NodeID), node.State(), claudeQuota, codexQuota)
	snapshot := func(snapshotCtx context.Context) (nodecontrol.Heartbeat, error) {
		archives := []string(nil)
		if inventory, ok := archiveVerifier.(archiveInventory); ok {
			archives, _ = inventory.ReadyArchiveIDs()
		}
		captureCtx, cancel := context.WithTimeout(snapshotCtx, 1500*time.Millisecond)
		interactive := localExecutor.InteractiveSnapshot(captureCtx)
		finals := collectTranscriptFinals(
			captureCtx, domain.NodeID(nodeConfig.NodeID), node.State().State(), transcripts,
		)
		cancel()
		return nodecontrol.Heartbeat{
			NodeID: nodeConfig.NodeID, BootID: bootID, Version: localBuildVersion(),
			OS: runtime.GOOS, Arch: runtime.GOARCH,
			Backends: inventory.Backends(), Archives: archives, Interactive: interactive,
			Finals: finals,
			Quotas: quotaStore.Snapshots(),
		}, nil
	}
	agent, err := nodecontrol.NewHeartbeatAgent(node, client, snapshot, nodeHeartbeatInterval)
	if err != nil {
		return err
	}
	monitor, err := nodecontrol.NewOfflineMonitor(
		node, node.State(), node, nodeOfflineTimeout, nodeOfflineSweep,
	)
	if err != nil {
		return err
	}
	recoveryExecutor, err := newFollowerRecoveryExecutor(
		node, nodeConfig, client, localExecutor, archiveVerifier,
	)
	if err != nil {
		return err
	}
	loopErrors := make(chan error, 4)
	recoveryPlans := make(chan domain.BootRecoveryPlan, 1)
	go inventory.Run(ctx)
	go quotaStore.Run(ctx)
	go agent.Run(ctx, loopErrors, recoveryPlans)
	go monitor.Run(ctx, loopErrors)
	go runRecoveryPlans(ctx, recoveryExecutor, recoveryPlans, loopErrors)
	go logHeartbeatErrors(ctx, loopErrors)
	return nil
}

func newFollowerRecoveryExecutor(
	node *consensus.Node,
	nodeConfig config.Config,
	client *nodecontrol.Client,
	localExecutor *runtimehost.LocalExecutor,
	archiveVerifier archiveVerifier,
) (*recovery.Executor, error) {
	remote, err := nodecontrol.NewRemoteRecoveryApplier(nodeConfig.NodeID, node, client)
	if err != nil {
		return nil, err
	}
	tmuxRuntime, err := runtimehost.NewTmuxRecoveryRuntime(
		runtimehost.ExecCommandRunner{}, nodeConfig.TmuxSession,
		map[string]runtimehost.BackendCommand{
			"claude": {Executable: nodeConfig.ClaudeCommand, Flags: nodeConfig.EffectiveClaudeFlags()},
			"codex":  {Executable: nodeConfig.CodexCommand, Flags: nodeConfig.EffectiveCodexFlags()},
		},
		30*time.Second,
	)
	if err != nil {
		return nil, err
	}
	runtime := attachingRecoveryRuntime{
		Runtime: tmuxRuntime, executor: localExecutor,
		nodeID: nodeConfig.NodeID, tmuxSession: nodeConfig.TmuxSession,
		archives: archiveVerifier,
	}
	return recovery.NewExecutor(domain.NodeID(nodeConfig.NodeID), node.State(), remote, runtime)
}

type recoveryRuntime interface {
	Resume(context.Context, domain.Session, string) error
}

type attachingRecoveryRuntime struct {
	Runtime     recoveryRuntime
	executor    *runtimehost.LocalExecutor
	nodeID      string
	tmuxSession string
	archives    archiveVerifier
}

type archiveVerifier interface {
	Verify(context.Context, domain.Session) error
}

type archiveInventory interface {
	ReadyArchiveIDs() ([]string, error)
}

func (r attachingRecoveryRuntime) Resume(
	ctx context.Context,
	session domain.Session,
	operationID string,
) error {
	binding := runtimehost.RuntimeBinding{
		NodeID: r.nodeID, SessionID: string(session.ID),
		Generation: session.RuntimeGeneration, Backend: session.Backend, Workdir: session.Workdir,
	}
	if err := r.executor.PrepareRecovery(binding); err != nil {
		return err
	}
	if session.ArchiveID != "" && session.ArchiveReason != "" {
		if r.archives == nil {
			return fmt.Errorf("native archive verifier is unavailable")
		}
		if err := r.archives.Verify(ctx, session); err != nil {
			return err
		}
	}
	if err := r.Runtime.Resume(ctx, session, operationID); err != nil {
		return err
	}
	return r.executor.Register(runtimehost.RuntimeBinding{
		NodeID: r.nodeID, SessionID: string(session.ID),
		Generation: session.RuntimeGeneration,
		TmuxTarget: runtimehost.TmuxTarget(r.tmuxSession, r.nodeID, string(session.ID)),
		Backend:    session.Backend, Workdir: session.Workdir,
	})
}

func runRecoveryPlans(
	ctx context.Context,
	executor *recovery.Executor,
	plans <-chan domain.BootRecoveryPlan,
	errorsOut chan<- error,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case plan := <-plans:
			if err := executor.Run(ctx, plan.Recover); err != nil && ctx.Err() == nil {
				select {
				case errorsOut <- err:
				default:
				}
			}
		}
	}
}

func logHeartbeatErrors(ctx context.Context, errorsIn <-chan error) {
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errorsIn:
			if err != nil {
				fmt.Fprintf(os.Stderr, "bria node heartbeat: %v\n", err)
			}
		}
	}
}

type nodeInventory struct {
	mu       sync.RWMutex
	backends []domain.BackendDescriptor
}

func newNodeInventory(backends []domain.BackendDescriptor) *nodeInventory {
	return &nodeInventory{backends: cloneBackends(backends)}
}

func (i *nodeInventory) Backends() []domain.BackendDescriptor {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return cloneBackends(i.backends)
}

func (i *nodeInventory) Run(ctx context.Context) {
	ticker := time.NewTicker(nodeInventoryRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			backends := discoverLocalBackends(ctx)
			i.mu.Lock()
			i.backends = cloneBackends(backends)
			i.mu.Unlock()
		}
	}
}

func cloneBackends(backends []domain.BackendDescriptor) []domain.BackendDescriptor {
	result := append([]domain.BackendDescriptor(nil), backends...)
	for index := range result {
		result[index].Capabilities = append([]string(nil), result[index].Capabilities...)
	}
	return result
}
