package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/providerbinding"
)

func runProviderHook(arguments []string) error {
	flags := flag.NewFlagSet("provider-hook", flag.ContinueOnError)
	configPath := flags.String("config", "", "absolute Bria config path")
	install := flags.Bool("install", false, "install the Bria Codex hooks")
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
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		return providerbinding.InstallHook(
			binary, *configPath, filepath.Join(home, ".codex", "hooks.json"),
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
	if err := providerbinding.Capture(
		ctx, store, os.Stdin, os.Getenv, providerbinding.DisplayTmux, time.Now,
	); err != nil {
		fmt.Fprintf(os.Stderr, "bria provider-hook: %v\n", err)
	}
	return nil
}
