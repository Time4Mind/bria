package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"bria/internal/provider/claude"
)

const (
	startModeEnv         = "BRIA_START_MODE"
	providerSessionIDEnv = "BRIA_PROVIDER_SESSION_ID"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	factory := &osProcessFactory{credentialPath: os.Getenv(claude.CredentialFileEnvironment)}
	exitCode := run(
		ctx,
		os.Args[1:],
		os.Stdin,
		os.Stdout,
		os.Stderr,
		rand.Reader,
		factory,
	)
	if err := factory.cleanupCurrentTree(); err != nil {
		exitCode = 1
	}
	os.Exit(exitCode)
}

func run(
	ctx context.Context,
	args []string,
	stdin io.ReadCloser,
	stdout io.Writer,
	stderr io.Writer,
	random io.Reader,
	factory claude.ProcessFactory,
) int {
	if len(args) < 2 || args[0] != "--" {
		fmt.Fprintln(stderr, "usage: bria-claude-adapter -- /absolute/path/to/claude [safe options]")
		return 2
	}
	args = args[1:]
	workdir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "working directory is unavailable")
		return 1
	}
	startMode := os.Getenv(startModeEnv)
	providerSessionID := os.Getenv(providerSessionIDEnv)
	var spec claude.CommandSpec
	switch startMode {
	case "new":
		if providerSessionID != "" {
			err = claude.ErrInvalidCommand
			break
		}
		spec, err = claude.BuildCommandSpec(args[0], args[1:], workdir, random)
	case "resume":
		spec, err = claude.BuildResumeCommandSpec(args[0], args[1:], workdir, providerSessionID)
	default:
		err = claude.ErrInvalidCommand
	}
	if err != nil {
		fmt.Fprintln(stderr, "invalid Claude adapter command")
		return 2
	}
	adapter, err := claude.NewAdapter(spec, stdin, stdout, factory, claude.AdapterOptions{})
	if err != nil {
		fmt.Fprintln(stderr, "invalid Claude adapter configuration")
		return 2
	}
	if err := adapter.Run(ctx); err != nil {
		fmt.Fprintln(stderr, "Claude adapter failed")
		return 1
	}
	return 0
}
