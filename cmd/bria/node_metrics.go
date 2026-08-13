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

func metricsNode(arguments []string) error {
	flags := flag.NewFlagSet("node metrics", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to node config JSON")
	target := flags.String("target", "", "node ID; defaults to the local node")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *configPath == "" || flags.NArg() != 0 {
		return errors.New("usage: bria node metrics --config PATH [--target NODE]")
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
		Certificate: certificate, Roots: roots, ClusterID: nodeConfig.ClusterID,
		Resolver: resolver, Timeout: 2 * time.Second,
	})
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	metrics, err := client.Metrics(ctx, *target)
	if err != nil {
		return err
	}
	fmt.Print(metrics)
	return nil
}
