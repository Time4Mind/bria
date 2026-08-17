package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/Time4Mind/bria/internal/archive"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/localarchive"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/platform"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/sessionname"
	"github.com/Time4Mind/bria/internal/transcript"
)

func startNodeRuntimeControl(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
	configPath string,
	certificate tls.Certificate,
	roots *x509.CertPool,
	backendRuntime backendRuntime,
	managedBackendRoots map[string]string,
) (*nodeRuntimeControl, error) {
	driver, err := runtimehost.NewTmuxDriver(
		backendRuntime.runner, 8*time.Second, 400*time.Millisecond, nil,
	)
	if err != nil {
		return nil, err
	}
	store, err := runtimehost.OpenBoltOperationStore(
		filepath.Join(nodeConfig.DataDir, "runtime", "operations.db"),
	)
	if err != nil {
		return nil, err
	}
	executor, err := runtimehost.NewLocalExecutor(nodeConfig.NodeID, driver, store)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	nameGenerator, err := sessionname.NewGenerator(
		backendRuntime.nameRunner, map[string]sessionname.Command{
			"claude": {Executable: nodeConfig.ClaudeCommand, Model: nodeConfig.ClaudeNamingModel},
			"codex":  {Executable: nodeConfig.CodexCommand, Model: nodeConfig.CodexNamingModel},
		}, 30*time.Second,
	)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	executor.SetNameGenerator(nameGenerator)
	if err := configureInboundResolver(executor, nodeConfig); err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	state := node.State().State()
	currentBootID, err := platform.NewBootIDProvider().Current(ctx)
	if err != nil {
		return closeFailedRuntime(executor, store, fmt.Errorf("read host boot id: %w", err))
	}
	knownBootID := state.Nodes[domain.NodeID(nodeConfig.NodeID)].BootID
	bootChanged := knownBootID != "" && knownBootID != currentBootID
	missing := make([]domain.SessionRef, 0)
	for _, session := range state.Sessions {
		if string(session.NodeID) != nodeConfig.NodeID || !session.IsLive() {
			continue
		}
		binding := runtimehost.RuntimeBinding{
			NodeID: nodeConfig.NodeID, SessionID: string(session.ID),
			Generation: session.RuntimeGeneration,
			TmuxTarget: runtimehost.TmuxTarget(nodeConfig.TmuxSession, nodeConfig.NodeID, string(session.ID)),
			Backend:    session.Backend, Workdir: session.Workdir,
		}
		if session.RuntimePhase == domain.RuntimeStarting {
			binding.TmuxTarget = ""
			err = executor.Prepare(binding)
		} else if bootChanged || session.ResumePending {
			binding.TmuxTarget = ""
			err = executor.Prepare(binding)
		} else {
			var exists bool
			exists, err = driver.TargetExists(ctx, binding.TmuxTarget)
			if err == nil && !exists {
				missing = append(missing, session.Ref())
				continue
			}
			if err == nil {
				err = executor.Register(binding)
			}
		}
		if err != nil {
			_ = executor.Shutdown(context.Background())
			_ = store.Close()
			return nil, fmt.Errorf("register local runtime %s: %w", session.Ref().Key(), err)
		}
	}
	guard, err := nodecontrol.NewStateGuard(node.State())
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	local, err := nodecontrol.NewService(nodeConfig.NodeID, guard, executor)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	resolver, err := controlResolver(nodeConfig, node.State())
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	client, err := nodecontrol.NewClient(nodecontrol.ClientConfig{
		Certificate: certificate, Roots: roots, ClusterID: nodeConfig.ClusterID,
		Resolver: resolver, Timeout: 750 * time.Millisecond,
	})
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	updates, err := prepareNodeUpdates(nodeConfig, configPath, client)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	home := backendRuntime.home
	transcriptReader, err := transcript.NewReader(transcript.Config{
		ClaudeProjectsRoot: filepath.Join(home, ".claude", "projects"),
		CodexSessionsRoot:  filepath.Join(home, ".codex", "sessions"),
		MaxReadBytes:       6 << 20,
		MaxBodyBytes:       64 << 10,
	})
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	localStarts, startRouter, err := newLocalSessionStart(
		node, nodeConfig, home, transcriptReader, executor, client, backendRuntime.runner,
	)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	go runLocalSessionStartReconciler(
		ctx, nodeConfig.NodeID, node.State(), localStarts, executor,
	)
	archiveStore, err := archive.NewFileStore(filepath.Join(nodeConfig.DataDir, "archives"))
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	archiveWriter, err := localarchive.NewWriter(archiveStore, transcriptReader)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	executor.SetArchiveWriter(archiveWriter)
	if err := reconcileLocalArchives(
		ctx, node.State().State(), nodeConfig, driver, archiveWriter,
	); err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	localTranscripts, err := nodecontrol.NewLocalTranscriptService(
		nodeConfig.NodeID, node.State(), transcriptReader, archiveWriter,
	)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	transcriptRouter, err := nodecontrol.NewTranscriptRouter(
		nodeConfig.NodeID, localTranscripts, client,
	)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	localSessionFiles, err := nodecontrol.NewLocalSessionFileService(nodeConfig.NodeID, node.State())
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	sessionFileRouter, err := nodecontrol.NewSessionFileRouter(
		nodeConfig.NodeID, localSessionFiles, client,
	)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	router, err := nodecontrol.NewRouter(nodeConfig.NodeID, local, client)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	localProviderAuth, providerAuthRouter, err := newProviderAuthentication(
		nodeConfig, guard, client, backendRuntime,
	)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	setups, err := newNodeSetupRuntime(
		ctx, nodeConfig, managedBackendRoots, backendRuntime, client,
	)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	heartbeats, err := nodecontrol.NewConsensusHeartbeatCommitter(node)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	enrollments, err := nodecontrol.NewConsensusEnrollmentCommitter(node)
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	server, err := nodecontrol.NewServer(nodecontrol.ServerConfig{
		NodeID: nodeConfig.NodeID, ClusterID: nodeConfig.ClusterID,
		Certificate: certificate, Roots: roots, Leadership: node,
		Health: node, Backups: node.State(), Admin: node,
		Membership: guard, Service: local, Heartbeats: heartbeats, Recovery: heartbeats,
		Transcripts: localTranscripts, SessionFiles: localSessionFiles, Starts: localStarts,
		ProviderAuth: localProviderAuth,
		BackendSetup: setups.localBackends,
		SpeechSetup:  setups.localSpeech,
		Updates:      updates.local,
		Enrollments:  enrollments, EnrollmentIssuerID: nodeConfig.EffectiveEnrollmentIssuerID(),
	})
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	bind, err := nodeConfig.ControlBindAddress()
	if err != nil {
		return closeFailedRuntime(executor, store, err)
	}
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		return closeFailedRuntime(executor, store, fmt.Errorf("listen for node control: %w", err))
	}
	control := &nodeRuntimeControl{
		router: router, transcripts: transcriptRouter, sessionFiles: sessionFileRouter,
		starts:       startRouter,
		providerAuth: providerAuthRouter, localProviderAuth: localProviderAuth,
		backendSetup: setups.backends,
		speechSetup:  setups.speech,
		updates:      updates,
		executor:     executor, store: store, client: client,
		server: server, listener: listener, errors: make(chan error, 1),
	}
	control.enrollment, err = startEnrollmentRuntime(
		ctx, node, nodeConfig, certificate, client, enrollments,
	)
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	go func() {
		control.errors <- server.Serve(listener)
		close(control.errors)
	}()
	if err := archiveMissingLocalSessions(ctx, node, nodeConfig.NodeID, client, missing); err != nil {
		_ = control.Close()
		return nil, err
	}
	if err := startNodeHeartbeatLoops(
		ctx, node, nodeConfig, client, executor, archiveWriter, transcriptReader,
		backendRuntime.runner,
		setups.inventory,
	); err != nil {
		_ = control.Close()
		return nil, err
	}
	go runLocalArchiveReconciler(ctx, node.State(), nodeConfig, driver, archiveWriter)
	go runLocalRuntimeReconciler(ctx, node, nodeConfig, client, executor, driver)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return control, nil
}

func archiveMissingLocalSessions(
	ctx context.Context,
	node *consensus.Node,
	nodeID string,
	client *nodecontrol.Client,
	refs []domain.SessionRef,
) error {
	if len(refs) == 0 {
		return nil
	}
	remote, err := nodecontrol.NewRemoteRecoveryApplier(nodeID, node, client)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		operationID, operationErr := newOperationID()
		if operationErr != nil {
			return operationErr
		}
		command, commandErr := clusterstate.NewCommand(
			operationID, clusterstate.CommandMarkMissing, time.Now(),
			clusterstate.MarkMissing{
				Session: ref, ArchiveID: clusterstate.MissingArchiveID(operationID),
			},
		)
		if commandErr != nil {
			return commandErr
		}
		var result clusterstate.Result
		if node.IsLeader() {
			result, err = node.Apply(ctx, command)
		} else {
			result, err = remote.Apply(ctx, command)
		}
		if errors.Is(err, nodecontrol.ErrRecoveryAlreadySettled) {
			// The leader has already closed or archived this session while this
			// follower was restarting. Let Raft catch the local projection up;
			// keeping the node offline cannot make that convergence happen.
			continue
		}
		if err != nil {
			return fmt.Errorf("archive missing runtime %s: %w", ref.Key(), err)
		}
		if err := result.Err(); err != nil {
			return fmt.Errorf("archive missing runtime %s: %w", ref.Key(), err)
		}
	}
	return nil
}
