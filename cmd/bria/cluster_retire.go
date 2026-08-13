package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
)

func retireClusterNode(arguments []string) error {
	flags := flag.NewFlagSet("cluster retire-node", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "current leader config path")
	nodeID := flags.String("node", "", "node ID to disable and delete")
	confirmation := flags.String("confirm", "", "repeat node ID")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *nodeID == "" || *confirmation != *nodeID {
		return errors.New("usage: bria cluster retire-node --config PATH --node NODE --confirm NODE")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *nodeID == nodeConfig.NodeID {
		return errors.New("cannot retire the local node")
	}
	resolver, err := controlResolver(nodeConfig, nil)
	if err != nil {
		return err
	}
	certificate, roots, err := loadNodeTLS(nodeConfig)
	if err != nil {
		return err
	}
	client, err := nodecontrol.NewClient(nodecontrol.ClientConfig{
		Certificate: certificate, Roots: roots, ClusterID: nodeConfig.ClusterID,
		Resolver: resolver, Timeout: 20 * time.Second,
	})
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := applyRetirementCommand(ctx, client, nodeConfig.NodeID, *nodeID,
		clusterstate.CommandSetNodeLifecycle,
		clusterstate.SetNodeLifecycle{NodeID: domain.NodeID(*nodeID), Lifecycle: domain.NodeDisabled},
	); err != nil {
		return fmt.Errorf("disable node: %w", err)
	}
	if err := applyRetirementCommand(ctx, client, nodeConfig.NodeID, *nodeID,
		clusterstate.CommandDeleteNode, clusterstate.DeleteNode{NodeID: domain.NodeID(*nodeID)},
	); err != nil {
		return fmt.Errorf("delete node: %w", err)
	}
	fmt.Printf("retired node %s\n", *nodeID)
	return nil
}

func applyRetirementCommand(
	ctx context.Context,
	client *nodecontrol.Client,
	leaderID, nodeID string,
	kind clusterstate.CommandKind,
	payload any,
) error {
	command, err := clusterstate.NewCommand(
		fmt.Sprintf("retire-%s-%s-%d", nodeID, kind, time.Now().UnixNano()),
		kind, time.Now(), payload,
	)
	if err != nil {
		return err
	}
	return client.ApplyMembershipCommand(ctx, leaderID, command)
}
