package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/runnerhost"
)

func checkNodeIsolation(arguments []string) error {
	flags := flag.NewFlagSet("node isolation-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to node config JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *configPath == "" || flags.NArg() != 0 {
		return errors.New("usage: bria node isolation-check --config PATH")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtime, err := openBackendRuntime(ctx, nodeConfig)
	if err != nil {
		return err
	}
	defer runtime.closer.Close()
	output := map[string]any{
		"mode": nodeConfig.EffectiveRunnerMode(), "isolated": nodeConfig.IsolatedRunner(),
		"home": runtime.home,
	}
	if nodeConfig.IsolatedRunner() {
		client, ok := runtime.runner.(*runnerhost.Client)
		if !ok {
			return errors.New("isolated runner client is unavailable")
		}
		inspect, inspectErr := client.Inspect(ctx)
		if inspectErr != nil {
			return inspectErr
		}
		output["runner"] = inspect
		output["summary"] = isolationSummary(nodeConfig.EffectiveRunnerMode(), inspect)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}
