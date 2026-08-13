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

func probeNode(arguments []string) error {
	flags := flag.NewFlagSet("node probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to node config JSON")
	target := flags.String("target", "", "node ID; defaults to the local node")
	healthOnly := flags.Bool("health-only", false, "check the process without quorum readiness")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *configPath == "" || flags.NArg() != 0 {
		return errors.New("usage: bria node probe --config PATH [--target NODE] [--health-only]")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *target == "" {
		*target = nodeConfig.NodeID
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
		Certificate: certificate,
		Roots:       roots,
		ClusterID:   nodeConfig.ClusterID,
		Resolver:    resolver,
		Timeout:     2 * time.Second,
	})
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	status, err := client.Probe(ctx, *target, !*healthOnly)
	if err != nil {
		return err
	}
	writeJSON(status)
	if status.Status == "not_ready" {
		return fmt.Errorf("node %s is not ready", *target)
	}
	return nil
}
