package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/platform"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/quota"
	"github.com/Time4Mind/bria/internal/recovery"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

const (
	nodeHeartbeatInterval = 5 * time.Second
	nodeOfflineTimeout    = 15 * time.Second
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
	runner runtimehost.JSONRPCCommandRunner,
	inventory *nodeInventory,
) error {
	bootID, err := platform.NewBootIDProvider().Current(ctx)
	if err != nil {
		return fmt.Errorf("read host boot id for heartbeat: %w", err)
	}
	if inventory == nil {
		return errors.New("node inventory is required")
	}
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
		state := node.State().State()
		archives := []string(nil)
		if inventory, ok := archiveVerifier.(archiveInventory); ok {
			archives, _ = inventory.ReadyArchiveIDs()
		}
		captureCtx, cancel := context.WithTimeout(snapshotCtx, 1500*time.Millisecond)
		interactive := localExecutor.InteractiveSnapshot(captureCtx)
		finals := collectTranscriptFinals(
			captureCtx, domain.NodeID(nodeConfig.NodeID), state, transcripts,
		)
		runtimeReports := []domain.TranscriptRuntimeReport(nil)
		if transcriptRuntimeHeartbeatEnabled(state, node.LeaderID(), localBuildVersion()) {
			runtimeReports = collectTranscriptRuntime(
				captureCtx, domain.NodeID(nodeConfig.NodeID), state, transcripts,
			)
		}
		cancel()
		return nodecontrol.Heartbeat{
			NodeID: nodeConfig.NodeID, BootID: bootID, Version: localBuildVersion(),
			OS: runtime.GOOS, Arch: runtime.GOARCH,
			Backends: inventory.Backends(), Archives: archives, Interactive: interactive,
			Finals: finals, Runtime: runtimeReports,
			Quotas: quotaStore.Snapshots(),
			BackendIsolation: domain.BackendIsolationReport{
				Mode: nodeConfig.EffectiveRunnerMode(), Ready: nodeConfig.IsolatedRunner(),
			},
		}, nil
	}
	leaders := heartbeatLeaderResolver{raft: node, state: node.State()}
	agent, err := nodecontrol.NewHeartbeatAgent(leaders, client, snapshot, nodeHeartbeatInterval)
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
		node, nodeConfig, client, localExecutor, archiveVerifier, runner,
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

type heartbeatLeaderSource interface {
	LeaderID() string
}

type heartbeatStateSource interface {
	State() *domain.State
}

// heartbeatLeaderResolver lets a parked manual replica report its return even
// after Raft has applied the configuration that removed it. Normal operation
// always follows Raft; the replicated manual preference is only a no-leader
// fallback and the destination still rejects heartbeats unless it is leader.
type heartbeatLeaderResolver struct {
	raft  heartbeatLeaderSource
	state heartbeatStateSource
}

func (r heartbeatLeaderResolver) LeaderID() string {
	if leaderID := r.raft.LeaderID(); leaderID != "" {
		return leaderID
	}
	state := r.state.State()
	if state == nil || state.LeaderPolicy.EffectiveMode() != domain.LeaderSelectionManual {
		return ""
	}
	leaderID := state.LeaderPolicy.NodeID
	leader, exists := state.Nodes[leaderID]
	if leaderID == "" || !exists || !leader.Enabled() {
		return ""
	}
	return string(leaderID)
}

func newFollowerRecoveryExecutor(
	node *consensus.Node,
	nodeConfig config.Config,
	client *nodecontrol.Client,
	localExecutor *runtimehost.LocalExecutor,
	archiveVerifier archiveVerifier,
	runner runtimehost.CommandRunner,
) (*recovery.Executor, error) {
	remote, err := nodecontrol.NewRemoteRecoveryApplier(nodeConfig.NodeID, node, client)
	if err != nil {
		return nil, err
	}
	tmuxRuntime, err := runtimehost.NewTmuxRecoveryRuntime(
		runner, nodeConfig.TmuxSession,
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
) (returnErr error) {
	startedAt := time.Now()
	heartbeatWait := restoreHeartbeatWait(session, startedAt)
	prepareDuration := time.Duration(0)
	verifyDuration := time.Duration(0)
	tmuxDuration := time.Duration(0)
	registerDuration := time.Duration(0)
	outcome := "prepare_failed"
	defer func() {
		logRestoreRuntimeTiming(
			session.Ref(), session.RuntimeGeneration, outcome, startedAt, heartbeatWait,
			prepareDuration, verifyDuration, tmuxDuration, registerDuration,
		)
	}()
	binding := runtimehost.RuntimeBinding{
		NodeID: r.nodeID, SessionID: string(session.ID),
		Generation: session.RuntimeGeneration, Backend: session.Backend, Workdir: session.Workdir,
	}
	phaseStartedAt := time.Now()
	if err := r.executor.PrepareRecovery(binding); err != nil {
		prepareDuration = time.Since(phaseStartedAt)
		return err
	}
	prepareDuration = time.Since(phaseStartedAt)
	if session.ArchiveID != "" && session.ArchiveReason != "" {
		if r.archives == nil {
			outcome = "archive_verify_failed"
			return fmt.Errorf("native archive verifier is unavailable")
		}
		phaseStartedAt = time.Now()
		if err := r.archives.Verify(ctx, session); err != nil {
			verifyDuration = time.Since(phaseStartedAt)
			outcome = "archive_verify_failed"
			return err
		}
		verifyDuration = time.Since(phaseStartedAt)
	}
	phaseStartedAt = time.Now()
	if err := r.Runtime.Resume(ctx, session, operationID); err != nil {
		tmuxDuration = time.Since(phaseStartedAt)
		outcome = "tmux_resume_failed"
		return err
	}
	tmuxDuration = time.Since(phaseStartedAt)
	phaseStartedAt = time.Now()
	returnErr = r.executor.Register(runtimehost.RuntimeBinding{
		NodeID: r.nodeID, SessionID: string(session.ID),
		Generation: session.RuntimeGeneration,
		TmuxTarget: runtimehost.TmuxTarget(r.tmuxSession, r.nodeID, string(session.ID)),
		Backend:    session.Backend, Workdir: session.Workdir,
	})
	registerDuration = time.Since(phaseStartedAt)
	if returnErr != nil {
		outcome = "register_failed"
		return returnErr
	}
	outcome = "ok"
	return nil
}

func restoreHeartbeatWait(session domain.Session, startedAt time.Time) time.Duration {
	// Archive restore resets LiveSinceAt when it is accepted, so this is the
	// queueing delay until the origin heartbeat starts recovery. Boot recovery
	// retains the original LiveSinceAt and must not be reported as heartbeat
	// latency for an unrelated, potentially old live session.
	if session.ArchiveID == "" || session.ArchiveReason == "" ||
		session.LiveSinceAt.IsZero() || !startedAt.After(session.LiveSinceAt) {
		return 0
	}
	return startedAt.Sub(session.LiveSinceAt)
}

func logRestoreRuntimeTiming(
	ref domain.SessionRef,
	generation uint64,
	outcome string,
	startedAt time.Time,
	heartbeatWait time.Duration,
	prepare time.Duration,
	verify time.Duration,
	tmuxResume time.Duration,
	register time.Duration,
) {
	total := time.Since(startedAt)
	format := "bria restore_timing: stage=runtime ref=%q generation=%d outcome=%s total_ms=%d heartbeat_wait_ms=%d prepare_ms=%d archive_verify_ms=%d tmux_resume_ms=%d register_ms=%d slow_restore=%t"
	arguments := []any{
		ref.Key(), generation, outcome, total.Milliseconds(), heartbeatWait.Milliseconds(),
		prepare.Milliseconds(), verify.Milliseconds(), tmuxResume.Milliseconds(),
		register.Milliseconds(), total > time.Second,
	}
	processlog.Detailf(format, arguments...)
	if total > time.Second {
		processlog.Servicef(format, arguments...)
	}
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
				processlog.Criticalf("bria node heartbeat: %v", err)
			}
		}
	}
}

type nodeInventory struct {
	mu       sync.RWMutex
	backends []domain.BackendDescriptor
	runner   runtimehost.CommandRunner
	commands backendCommands
}

func newNodeInventory(
	backends []domain.BackendDescriptor,
	runner runtimehost.CommandRunner,
	commands backendCommands,
) *nodeInventory {
	return &nodeInventory{
		backends: cloneBackends(backends), runner: runner, commands: commands,
	}
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
			i.Refresh(ctx)
		}
	}
}

func (i *nodeInventory) Refresh(ctx context.Context) {
	backends := discoverLocalBackends(ctx, i.runner, i.commands)
	i.mu.Lock()
	i.backends = cloneBackends(backends)
	i.mu.Unlock()
}

func cloneBackends(backends []domain.BackendDescriptor) []domain.BackendDescriptor {
	result := append([]domain.BackendDescriptor(nil), backends...)
	for index := range result {
		result[index].Capabilities = append([]string(nil), result[index].Capabilities...)
	}
	return result
}
