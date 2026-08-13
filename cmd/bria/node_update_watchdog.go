package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"strconv"
	"time"

	"github.com/Time4Mind/bria/internal/clusterupdate"
)

func runUpdateWatchdog(arguments []string) error {
	flags := flag.NewFlagSet("node update-watchdog", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	installRoot := flags.String("install-root", "", "managed install root")
	updateID := flags.String("update-id", "", "cluster update identity")
	pidValue := flags.String("pid", "", "node process id")
	timeout := flags.Duration("timeout", 90*time.Second, "rollback timeout")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return errors.New("usage: bria node update-watchdog --install-root PATH --update-id ID --pid PID")
	}
	pid, err := strconv.Atoi(*pidValue)
	if err != nil {
		return errors.New("update watchdog pid is invalid")
	}
	return clusterupdate.Watchdog(context.Background(), *installRoot, *updateID, pid, *timeout)
}
