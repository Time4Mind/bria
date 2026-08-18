package main

import (
	"errors"
	"flag"
	"io"

	"github.com/Time4Mind/bria/internal/config"
)

// checkNodeConfig is the stable pre-Raft update preflight. It intentionally
// needs neither a live node-control endpoint nor cluster TLS credentials.
func checkNodeConfig(arguments []string) error {
	flags := flag.NewFlagSet("node config-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to node config JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *configPath == "" || flags.NArg() != 0 {
		return errors.New("usage: bria node config-check --config PATH")
	}
	_, err := config.Load(*configPath)
	return err
}
