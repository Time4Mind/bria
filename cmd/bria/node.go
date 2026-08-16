package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/platform"
	"github.com/Time4Mind/bria/internal/recovery"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/security"
	"github.com/hashicorp/raft"
)

func runNode(arguments []string) error {
	if len(arguments) > 0 {
		switch arguments[0] {
		case "probe":
			return probeNode(arguments[1:])
		case "metrics":
			return metricsNode(arguments[1:])
		case "cert-request":
			return createCertificateRenewalRequest(arguments[1:])
		case "cert-install":
			return installCertificateRenewal(arguments[1:])
		case "cert-rollback":
			return rollbackCertificateRenewal(arguments[1:])
		case "update-watchdog":
			return runUpdateWatchdog(arguments[1:])
		case "isolation-check":
			return checkNodeIsolation(arguments[1:])
		}
	}
	if len(arguments) == 0 || arguments[0] != "run" {
		return errors.New("usage: bria node <run|probe|metrics|cert-request|cert-install|cert-rollback|update-watchdog|isolation-check>")
	}
	flags := flag.NewFlagSet("node run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to node config JSON")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if *configPath == "" || flags.NArg() != 0 {
		return errors.New("usage: bria node run --config PATH")
	}
	absoluteConfigPath, err := filepath.Abs(*configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	nodeConfig, err := config.Load(absoluteConfigPath)
	if err != nil {
		return err
	}
	backendRuntime, err := openBackendRuntime(context.Background(), nodeConfig)
	if err != nil {
		return fmt.Errorf("open backend runtime: %w", err)
	}
	defer backendRuntime.closer.Close()
	nodeConfig, managedBackendRoots := prepareManagedBackendCommands(nodeConfig, backendRuntime)
	certificate, roots, err := loadNodeTLS(nodeConfig)
	if err != nil {
		return err
	}
	localFingerprint, err := tlsCertificateFingerprint(certificate)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", nodeConfig.RaftBind)
	if err != nil {
		return fmt.Errorf("listen for raft: %w", err)
	}
	resolver := consensus.NewStaticPeerResolver()
	configurePeerResolver(resolver, nodeConfig)
	stream, err := consensus.NewTLSStreamLayer(consensus.TLSStreamConfig{
		Listener: listener, AdvertiseAddress: nodeConfig.RaftAdvertise,
		Certificate: certificate, Roots: roots, ClusterID: nodeConfig.ClusterID,
		LocalNodeID: nodeConfig.NodeID, Resolver: resolver,
	})
	if err != nil {
		_ = listener.Close()
		return err
	}
	transport := raft.NewNetworkTransport(stream, 3, 10*time.Second, os.Stderr)
	machine := clusterstate.NewMachine(nil)
	node, err := consensus.Open(consensus.Config{
		NodeID:       nodeConfig.NodeID,
		DataDir:      filepath.Join(nodeConfig.DataDir, "raft"),
		Bootstrap:    nodeConfig.Bootstrap,
		ApplyTimeout: 10 * time.Second,
		LogOutput:    os.Stderr,
	}, clusterstate.NewFSM(machine), transport)
	if err != nil {
		_ = transport.Close()
		return err
	}
	defer node.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	leaderCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = node.WaitForLeader(leaderCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("wait for cluster leader: %w", err)
	}
	if err := enforceLocalBackendIsolationPolicy(node.State().State(), nodeConfig); err != nil {
		return err
	}
	if nodeConfig.Bootstrap {
		if node.IsLeader() {
			if err := applyPendingClusterRestore(ctx, node, nodeConfig); err != nil {
				return err
			}
			// Commit the peer identity and network metadata while the existing
			// cluster still has its old quorum. Raft mTLS consults this replicated
			// admission state during snapshot transfer; adding a member first can
			// strand a one-node cluster before the new peer is trusted.
			if err := registerConfiguredNodes(ctx, node, nodeConfig); err != nil {
				return err
			}
			if err := reconcileConfiguredVoters(ctx, node, nodeConfig); err != nil {
				return err
			}
			plan, err := registerLocalNode(ctx, node, nodeConfig, localFingerprint, backendRuntime.runner)
			if err != nil {
				return err
			}
			if len(plan.Recover) > 0 {
				runtime, runtimeErr := runtimehost.NewTmuxRecoveryRuntime(
					backendRuntime.runner,
					nodeConfig.TmuxSession,
					map[string]runtimehost.BackendCommand{
						"claude": {Executable: nodeConfig.ClaudeCommand, Flags: nodeConfig.EffectiveClaudeFlags()},
						"codex":  {Executable: nodeConfig.CodexCommand, Flags: nodeConfig.EffectiveCodexFlags()},
					},
					30*time.Second,
				)
				if runtimeErr != nil {
					return runtimeErr
				}
				executor, recoveryErr := recovery.NewExecutor(
					domain.NodeID(nodeConfig.NodeID), node.State(), node, runtime,
				)
				if recoveryErr != nil {
					return recoveryErr
				}
				if recoveryErr := executor.Run(ctx, plan.Recover); recoveryErr != nil {
					// Every failed provider resume has already been committed as an
					// archive transition. Keep the healthy node online.
					fmt.Fprintf(os.Stderr, "bria reboot recovery: %v\n", recoveryErr)
				}
			}
		}
	}
	go maintainDynamicMembership(ctx, node, resolver, nodeConfig)
	runtimeControl, err := startNodeRuntimeControl(
		ctx, node, nodeConfig, absoluteConfigPath, certificate, roots, backendRuntime,
		managedBackendRoots,
	)
	if err != nil {
		return fmt.Errorf("start node runtime control: %w", err)
	}
	defer runtimeControl.Close()
	go maintainLeaderPolicy(ctx, node)
	updateCoordinator, err := startUpdateCoordinator(ctx, node, nodeConfig, runtimeControl)
	if err != nil {
		return fmt.Errorf("start cluster update coordinator: %w", err)
	}
	fmt.Fprintf(os.Stderr, "bria node %s running at %s\n", nodeConfig.NodeID, nodeConfig.RaftAdvertise)
	adapterErrors, err := startInteractionAdapters(ctx, node, nodeConfig, runtimeControl, updateCoordinator)
	if err != nil {
		return fmt.Errorf("start interaction adapters: %w", err)
	}
	confirmRunningUpdate(nodeConfig)
	return waitForNodeRuntime(ctx, runtimeControl, adapterErrors)
}

func loadNodeTLS(nodeConfig config.Config) (tls.Certificate, *x509.CertPool, error) {
	certificate, err := tls.LoadX509KeyPair(nodeConfig.NodeCertificate, nodeConfig.NodePrivateKey)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load node certificate: %w", err)
	}
	caPEM, err := os.ReadFile(nodeConfig.CACertificate)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("read cluster CA: %w", err)
	}
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return certificate, roots, nil
}

func tlsCertificateFingerprint(certificate tls.Certificate) (string, error) {
	if len(certificate.Certificate) == 0 {
		return "", errors.New("node certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return "", fmt.Errorf("parse node certificate leaf: %w", err)
	}
	return security.NodeCertificateFingerprint(leaf)
}

func registerLocalNode(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
	fingerprint string,
	runner runtimehost.CommandRunner,
) (domain.BootRecoveryPlan, error) {
	state := node.State().State()
	if _, exists := state.Nodes[domain.NodeID(nodeConfig.NodeID)]; !exists {
		operationID, err := newOperationID()
		if err != nil {
			return domain.BootRecoveryPlan{}, err
		}
		command, err := clusterstate.NewCommand(
			operationID, clusterstate.CommandAddNode, time.Now(),
			localDomainNode(nodeConfig, fingerprint),
		)
		if err != nil {
			return domain.BootRecoveryPlan{}, err
		}
		result, err := node.Apply(ctx, command)
		if err != nil {
			return domain.BootRecoveryPlan{}, fmt.Errorf("register bootstrap node: %w", err)
		}
		if err := result.Err(); err != nil {
			return domain.BootRecoveryPlan{}, fmt.Errorf("register bootstrap node: %w", err)
		}
	}
	if nodeConfig.BootstrapOwnerID > 0 {
		if err := ensureBootstrapOwner(ctx, node, nodeConfig); err != nil {
			return domain.BootRecoveryPlan{}, err
		}
	}
	runtimeOperationID, err := newOperationID()
	if err != nil {
		return domain.BootRecoveryPlan{}, err
	}
	runtimeCommand, err := clusterstate.NewCommand(
		runtimeOperationID,
		clusterstate.CommandUpdateNodeRuntime,
		time.Now(),
		clusterstate.UpdateNodeRuntime{
			NodeID: domain.NodeID(nodeConfig.NodeID), Status: domain.NodeOnline,
			Version: localBuildVersion(), Backends: connectedLocalBackends(
				node.State().State(), domain.NodeID(nodeConfig.NodeID),
				discoverLocalBackends(ctx, runner, configuredBackendCommands(nodeConfig))),
		},
	)
	if err != nil {
		return domain.BootRecoveryPlan{}, err
	}
	runtimeResult, err := node.Apply(ctx, runtimeCommand)
	if err != nil {
		return domain.BootRecoveryPlan{}, fmt.Errorf("publish node runtime: %w", err)
	}
	if err := runtimeResult.Err(); err != nil {
		return domain.BootRecoveryPlan{}, fmt.Errorf("publish node runtime: %w", err)
	}
	bootID, err := platform.NewBootIDProvider().Current(ctx)
	if err != nil {
		return domain.BootRecoveryPlan{}, fmt.Errorf("read host boot id: %w", err)
	}
	operationID, err := newOperationID()
	if err != nil {
		return domain.BootRecoveryPlan{}, err
	}
	command, err := clusterstate.NewCommand(
		operationID, clusterstate.CommandObserveBoot, time.Now(),
		clusterstate.ObserveBoot{NodeID: domain.NodeID(nodeConfig.NodeID), BootID: bootID},
	)
	if err != nil {
		return domain.BootRecoveryPlan{}, err
	}
	result, err := node.Apply(ctx, command)
	if err != nil {
		return domain.BootRecoveryPlan{}, fmt.Errorf("record node boot: %w", err)
	}
	if err := result.Err(); err != nil {
		return domain.BootRecoveryPlan{}, err
	}
	var plan domain.BootRecoveryPlan
	if err := json.Unmarshal(result.Value, &plan); err != nil {
		return domain.BootRecoveryPlan{}, fmt.Errorf("decode boot recovery plan: %w", err)
	}
	return plan, nil
}

func connectedLocalBackends(
	state *domain.State, nodeID domain.NodeID, installed []domain.BackendDescriptor,
) []domain.BackendDescriptor {
	if state == nil {
		return nil
	}
	node, ok := state.Nodes[nodeID]
	if !ok || !node.BackendSelectionInitialized {
		return nil
	}
	connected := make(map[string]bool, len(node.Backends))
	for _, backend := range node.Backends {
		connected[strings.ToLower(backend.Name)] = true
	}
	result := make([]domain.BackendDescriptor, 0, len(installed))
	for _, backend := range installed {
		if connected[strings.ToLower(backend.Name)] {
			result = append(result, backend)
		}
	}
	return result
}

func newOperationID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate operation id: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}
