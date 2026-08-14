package main

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
)

func localDomainNode(nodeConfig config.Config, fingerprint string) domain.Node {
	controlAddress, _ := nodeConfig.ControlAdvertiseAddress()
	enrollmentAddress := ""
	if nodeConfig.NodeID == nodeConfig.EffectiveEnrollmentIssuerID() {
		enrollmentAddress, _ = nodeConfig.EnrollmentAdvertiseAddress()
	}
	return domain.Node{
		ID: domain.NodeID(nodeConfig.NodeID), Name: nodeConfig.NodeName, Status: domain.NodeOnline,
		Lifecycle: domain.NodeActive, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Network: domain.NodeNetwork{
			RaftAddress: nodeConfig.RaftAdvertise, ControlAddress: controlAddress,
			EnrollmentAddress: enrollmentAddress,
		},
		Fingerprint: fingerprint,
		BackendIsolation: domain.BackendIsolationReport{
			Mode: nodeConfig.EffectiveRunnerMode(), Ready: nodeConfig.IsolatedRunner(),
		},
	}
}

func enforceLocalBackendIsolationPolicy(state *domain.State, nodeConfig config.Config) error {
	if state == nil {
		return errors.New("cluster state is unavailable")
	}
	node, ok := state.Nodes[domain.NodeID(nodeConfig.NodeID)]
	if !ok || !node.BackendIsolationRequired || nodeConfig.IsolatedRunner() {
		return nil
	}
	return fmt.Errorf(
		"node %s requires backend isolation; configure an isolated runner before restart",
		nodeConfig.NodeID,
	)
}
