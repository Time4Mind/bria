package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/nodecontrol"
)

func relocateClusterNode(arguments []string) error {
	flags := flag.NewFlagSet("cluster relocate-node", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "current leader config path")
	nodeID := flags.String("node", "", "node ID")
	raftAddress := flags.String("raft-address", "", "new Raft address")
	controlAddress := flags.String("control-address", "", "new control address")
	enrollmentAddress := flags.String("enrollment-address", "", "new enrollment address")
	confirmation := flags.String("confirm", "", "repeat node ID")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *nodeID == "" ||
		*raftAddress == "" || *controlAddress == "" || *confirmation != *nodeID {
		return errors.New("config, node, addresses, and exact confirmation are required")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
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
		Resolver: resolver, Timeout: 25 * time.Second,
	})
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.RelocateMembership(ctx, nodeConfig.NodeID, nodecontrol.MembershipRelocation{
		NodeID: *nodeID, RaftAddress: *raftAddress, ControlAddress: *controlAddress,
		EnrollmentAddress: *enrollmentAddress,
	}); err != nil {
		return err
	}
	fmt.Printf("relocated node %s to %s\n", *nodeID, *raftAddress)
	return nil
}
