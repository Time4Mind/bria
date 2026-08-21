package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/providerbinding"
	"github.com/Time4Mind/bria/internal/providerstop"
)

func runProviderHook(arguments []string) error {
	flags := flag.NewFlagSet("provider-hook", flag.ContinueOnError)
	configPath := flags.String("config", "", "absolute Bria config path")
	install := flags.Bool("install", false, "install the Bria provider hooks")
	backend := flags.String("backend", "codex", "provider backend (codex or claude)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*configPath) {
		return fmt.Errorf("usage: bria provider-hook --config PATH [--install]")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *install {
		binary := os.Getenv("BRIA_PROVIDER_HOOK_BINARY")
		if binary == "" {
			binary, err = os.Executable()
			if err != nil {
				return err
			}
		} else if !filepath.IsAbs(binary) {
			return errors.New("installed Bria hook binary path must be absolute")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		return providerbinding.InstallHooks(
			binary, *configPath,
			filepath.Join(home, ".codex", "hooks.json"),
			filepath.Join(home, ".claude", "settings.json"),
		)
	}
	store, err := providerbinding.NewStore(filepath.Join(nodeConfig.DataDir, "provider-bindings.json"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// Hooks must never block a provider prompt. Invalid or foreign hook events
	// are reported to stderr and deliberately return a successful exit status.
	result, captureErr := providerbinding.CaptureEvent(
		ctx, store, os.Stdin, os.Getenv, providerbinding.DisplayTmux, time.Now, *backend,
	)
	if captureErr != nil {
		fmt.Fprintf(os.Stderr, "bria provider-hook: %v\n", captureErr)
		return nil
	}
	if result.WakeFinal {
		if err := notifyProviderStop(ctx, nodeConfig, providerstop.Signal{
			NodeID: result.NodeID, SessionID: result.SessionID,
			ProviderSessionID: result.ProviderSessionID,
			RuntimeGeneration: result.RuntimeGeneration,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "bria provider-hook: notify final: %v\n", err)
		}
	}
	return nil
}

func notifyProviderStop(
	ctx context.Context,
	nodeConfig config.Config,
	signal providerstop.Signal,
) error {
	certificate, roots, err := loadNodeTLS(nodeConfig)
	if err != nil {
		return err
	}
	resolver, err := controlResolver(nodeConfig, nil)
	if err != nil {
		return err
	}
	client, err := nodecontrol.NewClient(nodecontrol.ClientConfig{
		Certificate: certificate, Roots: roots, ClusterID: nodeConfig.ClusterID,
		Resolver: resolver, Timeout: 1500 * time.Millisecond,
	})
	if err != nil {
		return err
	}
	if err := client.NotifyProviderStop(ctx, nodeConfig.NodeID, signal); err != nil {
		return err
	}
	return nil
}
