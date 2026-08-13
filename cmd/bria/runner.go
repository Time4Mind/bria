package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"syscall"
	"time"

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
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if *socket == "" || flags.NArg() != 0 {
		return errors.New("usage: bria runner serve --socket PATH")
	}
	server, err := runnerhost.NewServer(runtimehost.ExecCommandRunner{})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Printf("bria runner serving on %s\n", *socket)
	return server.Serve(*socket)
}
