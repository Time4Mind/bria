package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Time4Mind/bria/internal/providerbinding"
	"github.com/Time4Mind/bria/internal/runnerhost"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func runRunner(arguments []string) error {
	if len(arguments) == 0 || arguments[0] != "serve" {
		return errors.New("usage: bria runner serve --socket PATH")
	}
	flags := flag.NewFlagSet("runner serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", "", "Unix socket shared with the Bria control process")
	bindingStorePath := flags.String("binding-store", "", "runner-owned provider binding store")
	hookBinary := flags.String("hook-binary", "", "stable Bria binary used by provider hooks")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if *socket == "" || flags.NArg() != 0 || (*bindingStorePath == "") != (*hookBinary == "") {
		return errors.New("usage: bria runner serve --socket PATH [--binding-store PATH --hook-binary PATH]")
	}
	// Releases before runner-owned bindings supplied only --socket. Derive the
	// same stable paths so a binary-only cluster update can re-exec the existing
	// service unit safely; the explicit flags remain the deployment contract for
	// fresh installs.
	if *bindingStorePath == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return homeErr
		}
		activation, activationErr := resolveActivationPath()
		if activationErr != nil {
			return activationErr
		}
		*bindingStorePath = filepath.Join(home, ".bria", "provider-bindings.json")
		*hookBinary = activation
	}
	var bindings *providerbinding.Store
	var err error
	if *bindingStorePath != "" {
		bindings, err = providerbinding.NewStore(*bindingStorePath)
		if err != nil {
			return err
		}
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return homeErr
		}
		report, hookErr := providerbinding.ReconcileRunnerHooks(
			*hookBinary, *bindingStorePath,
			filepath.Join(home, ".codex", "hooks.json"),
			filepath.Join(home, ".claude", "settings.json"),
		)
		if hookErr != nil {
			return fmt.Errorf("reconcile isolated provider hooks: %w", hookErr)
		}
		if report.Changed {
			fmt.Printf("bria runner provider hooks reconciled migrations=%d\n", report.Migrations)
		}
	}
	server, err := runnerhost.NewServerWithBindings(runtimehost.ExecCommandRunner{}, bindings)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	releaseChanged := watchRunnerActivation(ctx)
	go func() {
		select {
		case <-ctx.Done():
		case <-releaseChanged:
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Printf("bria runner serving on %s\n", *socket)
	if err := server.Serve(*socket); err != nil {
		return err
	}
	select {
	case <-releaseChanged:
		// Re-exec in place instead of exiting.  bria-node.service requires the
		// runner service; letting systemd observe a runner failure can stop the
		// node and leave it inactive after an otherwise successful update.
		return reexecRunnerActivation()
	default:
		return nil
	}
}
