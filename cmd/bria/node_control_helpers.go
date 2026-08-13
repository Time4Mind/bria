package main

import (
	"context"
	"net"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func controlResolver(
	nodeConfig config.Config,
	reader nodecontrol.StateReader,
) (nodecontrol.Resolver, error) {
	self, err := nodeConfig.ControlAdvertiseAddress()
	if err != nil {
		return nil, err
	}
	addresses := map[string]string{nodeConfig.NodeID: self}
	overrides := make(map[string]string)
	for _, peer := range nodeConfig.RaftPeers {
		address, err := peer.EffectiveControlAddress()
		if err != nil {
			return nil, err
		}
		addresses[peer.NodeID] = address
		if peer.ControlDialAddress != "" {
			overrides[peer.NodeID] = peer.ControlDialAddress
		}
	}
	local, err := localControlAddress(nodeConfig)
	if err != nil {
		return nil, err
	}
	stateResolver := nodecontrol.NewStateResolver(
		reader,
		nodecontrol.NewStaticResolver(addresses),
		nodecontrol.NewStaticResolver(overrides),
	)
	return nodecontrol.NewLocalResolver(nodeConfig.NodeID, local, stateResolver), nil
}

func localControlAddress(nodeConfig config.Config) (string, error) {
	address, err := nodeConfig.ControlBindAddress()
	if err != nil {
		return "", err
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return net.JoinHostPort(host, port), nil
}

func closeFailedRuntime(
	executor *runtimehost.LocalExecutor,
	store *runtimehost.BoltOperationStore,
	err error,
) (*nodeRuntimeControl, error) {
	_ = executor.Shutdown(context.Background())
	_ = store.Close()
	return nil, err
}
